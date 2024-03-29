// TODO：性能优化：read和write不打开文件，file属性里面存一个*os.File
// TODO：增加access time，根据访问时间进行缓存的删除
// TODO：支持目录和权限修改
// TODO：支持连接
// TODO：通过文件名保存文件大小
// TODO：优化同时打开同一个文件的问题（并发读）
package main

import (
	"bytes"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"bazil.org/fuse"
	"bazil.org/fuse/fs"
	"golang.org/x/net/context"
)

// 记录当前已分配到的inode数字，注意，inode从1开始，而不是0！
var allocatedInode uint64

// 返回一个新的 inode
func NewInode() uint64 {
	allocatedInode += 1
	return allocatedInode
}

// 文件系统
var filesys FS

// inode到对象的索引
var inodeMap = make(map[uint64]interface{})

// 定义一个“文件系统”结构体 https://godoc.org/bazil.org/fuse/fs#FS
type FS struct {
	root *Dir // 这里我们给这个结构体加了一个root属性，其值为一个目录结构体，表示根目录
}

// 文件系统必须包含Root()方法 https://godoc.org/bazil.org/fuse/fs#FS
func (f *FS) Root() (fs.Node, error) {
	return f.root, nil // Root方法返回文件系统根目录
	// return &Dir{files: f}, nil
}

// Node结构体，必须包含Attr()方法 https://godoc.org/bazil.org/fuse/fs#Node
type Node struct {
	inode uint64
	//parent_inode uint64
	name     string
	fullPath string //所在目录路径。如果是目录，则fullPath包含自身名称。FUSE根目录为空，其绝对路径为BackendDir+fullPath（BackendDir和fullPath都以/结尾）
}

// 目录结构体，自定义的，继承了Node结构体，一个目录下包含一些文件和目录
type Dir struct {
	Node
	files       map[string]*File
	directories map[string]*Dir
}

// 文件结构体，自定义的，继承了Node结构体
type File struct {
	Node
	rawPath   string   //如果为空，说明没有解压。解压后这里设置为解压后的路径。release时再压缩写入。
	modified  bool     //如果为true，release后压缩写入，否则不进行操作
	file      *os.File // 文件指针
	flag      int      // 文件打开的Flag，参考：https://godoc.org/bazil.org/fuse#OpenFlags
	openCount int      // 文件同时打开的次数，Open的时候+1，Relase的时候-1，如果为0，再删除解压后的文件
}

// 目录结构体的Attr()方法，返回目录属性
func (d *Dir) Attr(ctx context.Context, a *fuse.Attr) error {
	//fmt.Println("[Attr]", d.name)
	a.Inode = d.inode
	a.Mode = os.ModeDir | 0555 // TODO: 优化权限
	return nil
}

// 文件结构体的Attr()方法，返回文件属性 https://godoc.org/bazil.org/fuse#Attr
func (f *File) Attr(ctx context.Context, a *fuse.Attr) error {
	// TODO: 支持所有attr
	//TODO：增加access time，根据访问时间进行缓存的删除
	fmt.Println("[Attr]", f.fullPath+f.name, "Inode:", f.inode)
	a.Inode = f.inode

	//打开压缩文件
	fr, err := os.Open(BackendDir + f.fullPath + f.name)
	if err != nil {
		fmt.Println("[ERROR]Attr打开文件失败！", err)
		return err
	}
	defer fr.Close()

	//读取基本信息
	fileInfo, _ := fr.Stat()
	a.Mode = fileInfo.Mode()
	a.Mtime = fileInfo.ModTime()

	// 获取文件大小
	if f.rawPath != "" {
		a.Size = getFileSize(f.rawPath)
	} else {
		//打开读取器
		r, err := NewReader(fr)
		if err != nil {
			fmt.Println("[getFileSize ERROR, NewReader]", err)
			a.Size = uint64(0)
		}
		defer r.Close()
		//读取压缩文件
		buf := bytes.NewBuffer(nil)
		io.Copy(buf, r)
		a.Size = uint64(buf.Len())
	}
	return nil
}

// 修改文件属性 https://godoc.org/bazil.org/fuse/fs#NodeSetattrer
func (f *File) Setattr(ctx context.Context, req *fuse.SetattrRequest, resp *fuse.SetattrResponse) error {
	// TODO: 支持所有attr
	fmt.Println("[Setattr]", f.fullPath+f.name, "Inode:", f.inode)
	fmt.Println(req)

	if req.Valid.Size() {
		// 不知道是不是一定会先open文件才truncate的，如果不是，这里需要判断f.file是否打开，如果没打开，则需要打开，然后再truncate
		err := f.file.Truncate(int64(req.Size))
		if err != nil {
			fmt.Println("[ERROR]Setattr Size", err.Error())
		}
	}
	return nil
}

// 查找目录下有没有这个文件或目录，返回对应的node https://godoc.org/bazil.org/fuse/fs#NodeStringLookuper
func (d *Dir) Lookup(ctx context.Context, name string) (fs.Node, error) {
	//fmt.Println("[Lookup]Dir:", d.name, "Name:", name)
	// 如果目录下有文件
	if len(d.files) > 0 {
		for _, v := range d.files {
			// 找到这个 Node 则返回
			if v.name == name {
				return v, nil
			}
		}
	}
	// 如果目录下有目录
	if len(d.directories) > 0 {
		for _, v := range d.directories {
			// 找到这个 Node 则返回
			if v.name == name {
				return v, nil
			}
		}
	}
	// 找不到对应的文件或目录，返回 ENOENT
	// ENOENT 即 Error NO ENTry/ENTity 即 没有这样的文件或目录
	return nil, fuse.ENOENT
}

// 列目录，返回Dirent列表（A Dirent represents a single directory entry.）
// https://godoc.org/bazil.org/fuse/fs#HandleReadAller
// https://godoc.org/bazil.org/fuse#Dirent
func (d *Dir) ReadDirAll(ctx context.Context) ([]fuse.Dirent, error) {
	//fmt.Println("[ReadDirAll]", d.name)
	var children []fuse.Dirent
	// 遍历文件
	if len(d.files) > 0 {
		for _, f := range d.files {
			children = append(children, fuse.Dirent{
				Inode: f.inode,
				Type:  fuse.DT_File,
				Name:  f.name})
		}
	}
	// 遍历目录
	if len(d.directories) > 0 {
		for _, dir := range d.directories {
			children = append(children, fuse.Dirent{
				Inode: dir.inode,
				Type:  fuse.DT_Dir,
				Name:  dir.name})
		}
	}
	// 返回列表
	return children, nil
}

// 创建文件 https://godoc.org/bazil.org/fuse/fs#NodeCreater
func (d *Dir) Create(ctx context.Context, req *fuse.CreateRequest, resp *fuse.CreateResponse) (fs.Node, fs.Handle, error) {
	fmt.Println("[Create]Dir:", d.fullPath, "Name:", req.Name)
	// 定义文件路径
	path := BackendDir + d.fullPath + req.Name // 压缩后的存放路径
	rawPath := path + ".compressfs.raw"        // 解压后的存放路径
	// 创建文件
	fc, err := os.Create(path)
	defer fc.Close()
	if err != nil {
		fmt.Println("[ERROR]创建文件失败！", err.Error())
	}
	// 创建raw文件
	fc2, err := os.Create(rawPath) // 暂时不Close（Create和Open一样，需要返回Handle，所以不能Close。）
	if err != nil {
		fmt.Println("[ERROR]创建文件失败！", err.Error())
	}
	// 构造一个文件结构体
	inode := NewInode()
	f := &File{
		Node: Node{
			name:     req.Name,
			inode:    inode,
			fullPath: d.fullPath,
		},
		rawPath:   rawPath,
		modified:  false,
		file:      fc2,
		flag:      os.O_WRONLY | os.O_CREATE,
		openCount: 1,
	}
	inodeMap[inode] = f
	// 把文件加到目录的文件map里
	d.files[f.name] = f
	// 返回Node
	return f, f, nil
}

// 删除文件或目录 https://godoc.org/bazil.org/fuse#RemoveRequest
func (d *Dir) Remove(ctx context.Context, req *fuse.RemoveRequest) error {
	fmt.Println("[Remove]Dir:", d.fullPath, "Name:", req.Name, "Dir:", req.Dir)
	err := os.Remove(BackendDir + d.fullPath + req.Name) // 删除文件或空目录
	if err != nil {
		fmt.Println(err, BackendDir+d.fullPath+req.Name)
	}
	// 如果是删除目录，判断是不是有目录；如果删除文件，先看有没有文件
	if req.Dir && len(d.directories) > 0 {
		delete(d.directories, req.Name)
		return nil
	} else if !req.Dir && len(d.files) > 0 {
		delete(d.files, req.Name)
		return nil
	}
	// 返回 ENOENT
	return fuse.ENOENT
}

// 创建目录 https://godoc.org/bazil.org/fuse/fs#NodeMkdirer
func (d *Dir) Mkdir(ctx context.Context, req *fuse.MkdirRequest) (fs.Node, error) {
	fmt.Println("[Mkdir]", d.fullPath, "Name:", req.Name, "Mode:", req.Mode)
	path := BackendDir + d.fullPath + req.Name
	// 创建目录
	os.Mkdir(path, req.Mode)
	// 构造一个目录结构体
	inode := NewInode()
	f := &Dir{
		Node: Node{
			name:     req.Name,
			inode:    inode,
			fullPath: d.fullPath + req.Name + "/",
		},
		files:       make(map[string]*File),
		directories: make(map[string]*Dir),
	}
	inodeMap[inode] = f
	// 把新建的目录加到目录的目录列表里
	d.directories[f.name] = f
	return f, nil
}

// 打开文件 https://godoc.org/bazil.org/fuse/fs#NodeOpener
func (f *File) Open(ctx context.Context, req *fuse.OpenRequest, resp *fuse.OpenResponse) (fs.Handle, error) {
	fmt.Println("[Open]", f.fullPath+f.name, "Inode:", f.inode, "Dir:", req.Dir, "Flags:", req.Flags)
	// TODO: 允许多文件同时打开，目前禁止并发读，直接阻塞
	for f.openCount > 0 {
		time.Sleep(time.Millisecond)
	}
	// 并发打开计数器+1
	f.openCount += 1
	// 定义路径
	path := BackendDir + f.fullPath + f.name
	rawPath := path + ".compressfs.raw"
	// 如果未解压，则解压
	if f.rawPath == "" {
		f.rawPath = rawPath
		// 打开压缩文件
		fz, err := os.Open(path)
		defer fz.Close()
		if err != nil {
			fmt.Println("[ERROR]打开压缩文件错误", err)
		}
		// 创建解压后的文件
		fr, err := os.OpenFile(rawPath, os.O_WRONLY|os.O_CREATE, 0600)
		if err != nil {
			fmt.Println("[ERROR]创建解压文件错误", err)
		}
		// 打开读取器
		r, err := NewReader(fz)
		if err != nil {
			fmt.Println(err.Error())
			return nil, nil
		}
		defer r.Close()
		// 解压文件
		io.Copy(fr, r)
		// 关闭文件
		fr.Close()
	}
	// 如果文件没被打开过，则打开文件
	if f.file == nil {
		flags := int(req.Flags)
		fr, err := os.OpenFile(rawPath, flags, 0755)
		if err != nil {
			fmt.Println("[ERROR]打开解压后的文件错误", err)
		}
		f.file = fr
		f.flag = int(req.Flags)
		/* TODO：优化并发读
		// 如果是RDONLY，则复制一个Node返回，否则把文件指针赋值到原Node的file
		if int(req.Flags) == os.O_RDONLY {
			fr, err := os.OpenFile(rawPath, os.O_RDONLY, 0755)
			if err != nil {
				fmt.Println("[ERROR]打开解压后的文件错误", err)
			}
			// 只读模式，构造一个新的Node返回，从而实现可以多次Open文件进行读取
			fn := &File{
				Node: Node{
					name:     f.name,
					inode:    f.inode,
					fullPath: f.fullPath,
				},
				rawPath: f.rawPath,
				file:    fr,
				flag:    os.O_RDONLY,
			} // os.O_RDONLY == 0 详见：https://godoc.org/syscall#O_RDONLY
			//返回文件Handle
			return fn, nil
		} else {
			flags := int(req.Flags)
			fr, err := os.OpenFile(rawPath, flags, 0755)
			if err != nil {
				fmt.Println("[ERROR]打开解压后的文件错误", err)
			}
			f.file = fr
			f.flag = int(req.Flags)
		}
		*/
	}
	//返回文件Handle
	return f, nil
}

// 读取文件 https://godoc.org/bazil.org/fuse/fs#HandleReader
func (f *File) Read(ctx context.Context, req *fuse.ReadRequest, resp *fuse.ReadResponse) error {
	fmt.Println("[Read]", f.fullPath+f.name, "Inode:", f.inode, "Dir:", req.Dir, "Size:", req.Size, "Offset:", req.Offset)
	// 读取解压后的文件，赋值到 resp.Data
	f.file.ReadAt(resp.Data[:req.Size], req.Offset)
	// 调整切片长度，详见切片机制：https://blog.csdn.net/u013474436/article/details/88770501
	resp.Data = resp.Data[:req.Size]
	return nil
}

// 写入文件 https://godoc.org/bazil.org/fuse/fs#HandleWriter 【文件变小的时候会有bug】
func (f *File) Write(ctx context.Context, req *fuse.WriteRequest, resp *fuse.WriteResponse) error {
	resp.Size = len(req.Data)
	fmt.Println("[Write]", f.fullPath+f.name, "Inode:", f.inode, "Size:", resp.Size, "Offset:", req.Offset, "Flags:", req.Flags, "FileFlags:", req.FileFlags)
	// 如果flag是只读，则返回错误
	if f.flag == os.O_RDONLY {
		return fuse.EPERM // EPERM 操作不允许 参考：https://godoc.org/bazil.org/fuse#pkg-constants https://blog.csdn.net/a8039974/article/details/25830705
	}
	// 文件标记为被修改
	f.modified = true
	// 写入文件
	//fmt.Println(req.Data)
	_, err := f.file.WriteAt(req.Data, req.Offset) // BUG: 文件大小可扩不可缩 写入的数据总是一堆0（py试试？）通过python的fuse测试，发现go的fuse缺少truncate操作
	//fmt.Println("Write", n, "bytes")
	if err != nil {
		fmt.Println("[ERROR]写入文件错误", err)
	}
	return nil
}

// 释放文件 https://godoc.org/bazil.org/fuse/fs#HandleReleaser
func (f *File) Release(ctx context.Context, req *fuse.ReleaseRequest) error {
	f.openCount -= 1
	fmt.Println("[Release]", f.fullPath+f.name, "Inode:", f.inode, "rawPath:", f.rawPath, "openCount:", f.openCount, "modified:", f.modified)
	// 如果是只读，直接返回 TODO: 优化并发读
	// if f.flag == os.O_RDONLY {
	// 	err := f.file.Close()
	// 	if err != nil {
	// 		fmt.Println("Close file error:", err)
	// 	}
	// }
	// 定义路径变量
	path := BackendDir + f.fullPath + f.name
	rawPath := path + ".compressfs.raw"
	// 如果文件被修改了，就重新压缩
	if f.modified == true {
		// 打开压缩文件
		fz, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0600)
		defer fz.Close()
		if err != nil {
			fmt.Println("[RROR]打开压缩文件失败！", f.name, err.Error())
			return nil
		}
		// 打开写入器
		w, err := NewWriter(fz)
		if err != nil {
			fmt.Println(err.Error())
			return nil
		}
		defer w.Close()
		// 关闭解压后的文件并重新打开
		f.file.Close()
		fr, err := os.OpenFile(rawPath, os.O_RDONLY, 0600)
		defer fr.Close()
		if err != nil {
			fmt.Println("[ERROR]打开解压后文件失败！", f.name, err.Error())
			return nil
		}
		// 写入
		io.Copy(w, fr)
		// 文件变成未修改
		f.modified = false
		// 清空文件指针和flag
		f.file = nil
		f.flag = 0
	}
	// 如果openCount为0，则删除解压后的文件
	if f.openCount == 0 {
		err := os.Remove(f.rawPath)
		if err != nil {
			fmt.Println("os.Remove error:", err)
		}
		f.rawPath = ""
	}
	return nil
}

// 同步文件修改到磁盘 https://godoc.org/bazil.org/fuse/fs#HandleFlusher
func (f *File) Flush(ctx context.Context, req *fuse.FlushRequest) error {
	fmt.Println("[Flush]", f.fullPath+f.name)
	f.file.Sync()
	return nil
}

// fsync（也是同步到磁盘） https://godoc.org/bazil.org/fuse/fs#NodeFsyncer
func (f *File) Fsync(ctx context.Context, req *fuse.FsyncRequest) error {
	fmt.Println("[Fsync]", f.fullPath+f.name)
	f.file.Sync()
	return nil
}

// 重命名 仅是Dir结构体的方法（文档未写明） https://godoc.org/bazil.org/fuse/fs#NodeRenamer
func (d *Dir) Rename(ctx context.Context, req *fuse.RenameRequest, newDir fs.Node) error {
	fmt.Println("[Rename]", d.name, req, newDir)
	dstDir := inodeMap[uint64(req.NewDir)].(*Dir)
	oldLocation := BackendDir + d.fullPath + req.OldName
	newLocation := BackendDir + dstDir.fullPath + req.NewName
	err := os.Rename(oldLocation, newLocation)
	if err != nil {
		fmt.Println(err)
		return err
	}
	s, err := os.Stat(newLocation)
	if err != nil {
		fmt.Println("os.Stat error:", err)
		return err
	}
	if s.IsDir() {
		dstDir.directories[req.NewName] = d.directories[req.OldName]
		dstDir.directories[req.NewName].name = req.NewName
		dstDir.directories[req.NewName].fullPath = dstDir.fullPath + req.NewName + "/"
		delete(d.directories, req.OldName)
	} else {
		dstDir.files[req.NewName] = d.files[req.OldName]
		dstDir.files[req.NewName].name = req.NewName
		dstDir.files[req.NewName].fullPath = dstDir.fullPath
		delete(d.files, req.OldName)
	}
	return nil
}

// ****************************************

// 遍历目录，返回Dir结构体
func readDir(name string, path string) *Dir {
	dirInfos, err := ioutil.ReadDir(path) //读取目录文件名
	if err != nil {
		fmt.Println("[ERROR]目录打开错误！", err)
		os.Exit(1)
	}
	fullPath := strings.TrimSuffix(strings.TrimPrefix(path, BackendDir), "/") + "/"
	inode := NewInode()
	dir := &Dir{
		Node: Node{
			name:     name,
			inode:    inode,
			fullPath: fullPath,
		},
		files:       make(map[string]*File),
		directories: make(map[string]*Dir),
	}
	inodeMap[inode] = dir
	for _, f := range dirInfos {
		if f.IsDir() {
			newDir := readDir(f.Name(), path+f.Name()+"/")
			dir.directories[newDir.name] = newDir
		} else {
			// 如果是解压后的文件，则删除
			if strings.HasSuffix(f.Name(), ".compressfs.raw") {
				os.Remove(BackendDir + dir.fullPath + f.Name())
				continue
			}
			// 否则添加到文件列表
			inode := NewInode()
			file := &File{
				Node: Node{
					name:     f.Name(),
					inode:    inode,
					fullPath: dir.fullPath,
				},
			}
			inodeMap[inode] = file
			dir.files[file.name] = file
		}
	}
	return dir
}

// 启动 FUSE
func run() error {
	// Enable debug
	// fuse.Debug = func(msg interface{}) {
	// 	fmt.Println(msg)
	// }

	c, err := fuse.Mount( // see: https://godoc.org/bazil.org/fuse#MountOption
		Mountpoint,
		fuse.FSName("compressfs"),
		fuse.Subtype("compressfs"),
	)
	if err != nil {
		return err
	}
	defer c.Close()

	// 优雅退出
	exitChan := make(chan os.Signal)
	signal.Notify(exitChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		for {
			select {
			case sig := <-exitChan:
				fmt.Println("Received signal:", sig)
				err := fuse.Unmount(Mountpoint)
				if err != nil {
					fmt.Println("Umount error:", err)
				}
			}
		}
	}()

	// 判断协议版本支持
	if p := c.Protocol(); !p.HasInvalidate() {
		return fmt.Errorf("kernel FUSE support is too old to have invalidations: version %v", p)
	}

	// 检查参数（如果 BackendDir 不是以斜杠结尾，则加上斜杠）
	if !strings.HasSuffix(BackendDir, "/") {
		BackendDir = BackendDir + "/"
	}

	// 初始化根文件系统
	inode := NewInode()
	filesys.root = &Dir{
		Node: Node{
			inode:    inode,
			name:     "",
			fullPath: "",
		},
		files:       make(map[string]*File),
		directories: make(map[string]*Dir),
	}
	inodeMap[inode] = filesys.root

	// 读取 BackendDir
	fmt.Println("[run]读取BackendDir")
	dirInfos, err := ioutil.ReadDir(BackendDir) //读取目录文件名
	if err != nil {
		fmt.Println("[ERROR]BackendDir打开错误！", err)
		os.Exit(1)
	}
	for _, f := range dirInfos {
		if f.IsDir() {
			newDir := readDir(f.Name(), BackendDir+f.Name()+"/")
			filesys.root.directories[newDir.name] = newDir
		} else {
			// 如果是解压后的文件，则删除
			if strings.HasSuffix(f.Name(), ".compressfs.raw") {
				os.Remove(BackendDir + f.Name())
				continue
			}
			// 否则添加到文件列表
			inode := NewInode()
			file := &File{
				Node: Node{
					name:     f.Name(),
					inode:    inode,
					fullPath: "",
				},
			}
			inodeMap[inode] = file
			filesys.root.files[file.name] = file
		}
	}

	// 调用 Serve
	fmt.Println("[run]调用Serve")
	srv := fs.New(c, nil)
	if err := srv.Serve(&filesys); err != nil {
		return err
	}

	return nil
}
