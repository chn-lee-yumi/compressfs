// TODO:解决多层目录的问题
// TODO：支持修改权限
// TODO：支持连接
// TODO：支持重命名
package main

import (
	"bazil.org/fuse"
	"bazil.org/fuse/fs"
	"bytes"
	"fmt"
	"golang.org/x/net/context"
	"io"
	"io/ioutil"
	"os"
)

// 启动 FUSE
func run() error {
	c, err := fuse.Mount( // see: https://godoc.org/bazil.org/fuse#MountOption
		Mountpoint,
		fuse.FSName("compressfs"),
		fuse.Subtype("compressfs"),
		fuse.LocalVolume(),
		fuse.VolumeName("compressfs filesystem"),
	)
	if err != nil {
		return err
	}
	defer c.Close()

	if p := c.Protocol(); !p.HasInvalidate() {
		return fmt.Errorf("kernel FUSE support is too old to have invalidations: version %v", p)
	}

	// 读取 BackendDir
	fmt.Println("[run]读取BackendDir")
	var files []*File
	var dirs []*Dir
	dir, err := ioutil.ReadDir(BackendDir) //读取目录文件名
	if err != nil {
		fmt.Println("[ERROR]BackendDir打开错误！", err)
		os.Exit(1)
	}
	fmt.Println("[run]给files赋值")
	for _, f := range dir {
		if f.IsDir() {
			dirs = append(dirs, &Dir{
				Node: Node{
					name:  f.Name(),
					inode: NewInode()}})
		} else {
			files = append(files, &File{
				Node: Node{
					name:  f.Name(),
					inode: NewInode()}})
		}
	}

	// 初始化根文件系统
	filesys := &FS{
		root: &Dir{
			Node:        Node{name: "/"},
			files:       &files,
			directories: &dirs}}

	// 调用 Serve
	fmt.Println("[run]调用Serve")
	srv := fs.New(c, nil)
	if err := srv.Serve(filesys); err != nil {
		return err
	}

	// Check if the mount process has an error to report.
	<-c.Ready
	if err := c.MountError; err != nil {
		return err
	}
	return nil
}

// 文件系统 inode
var inode uint64

// 返回一个新的 inode
func NewInode() uint64 {
	inode += 1
	return inode
}

/**********************************************/

// https://gist.github.com/crgimenes/4740c19f00989823a30881f0916edf02

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
	name string
}

// 目录结构体，自定义的，继承了Node结构体，一个目录下包含一些文件和目录
type Dir struct {
	Node
	files       *[]*File
	directories *[]*Dir
}

// 文件结构体，自定义的，继承了Node结构体
type File struct {
	Node
	rawPath  string   //如果为空，说明没有解压。解压后这里设置为解压后的路径。release时再压缩写入。
	modified bool     //如果为true，release后压缩写入，否则不进行操作
	file     *os.File // 文件指针
	flag     int      // 文件打开的Flag，参考：https://godoc.org/bazil.org/fuse#OpenFlags
}

// 目录结构体的Attr()方法，返回目录属性
func (d *Dir) Attr(ctx context.Context, a *fuse.Attr) error {
	//fmt.Println("[Attr]", d.name)
	a.Inode = d.inode
	a.Mode = os.ModeDir | 0555 // TODO: 优化权限
	return nil
}

// 文件结构体的Attr()方法，返回文件属性
func (f *File) Attr(ctx context.Context, a *fuse.Attr) error {
	//TODO：增加access time，根据访问时间进行缓存的删除
	//fmt.Println("[Attr]", f.name)
	a.Inode = f.inode

	//打开压缩文件
	fr, _ := os.Open(BackendDir + f.name)
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

// 查找目录下有没有这个文件或目录，返回对应的node https://godoc.org/bazil.org/fuse/fs#NodeStringLookuper
func (d *Dir) Lookup(ctx context.Context, name string) (fs.Node, error) {
	//fmt.Println("[Lookup]Dir:", d.name, "Name:", name)
	// 如果目录下有文件
	if d.files != nil {
		for _, n := range *d.files {
			// 找到这个 Node 则返回
			if n.name == name {
				return n, nil
			}
		}
	}
	// 如果目录下有目录
	if d.directories != nil {
		for _, n := range *d.directories {
			// 找到这个 Node 则返回
			if n.name == name {
				return n, nil
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
	if d.files != nil {
		for _, f := range *d.files {
			children = append(children, fuse.Dirent{
				Inode: f.inode,
				Type:  fuse.DT_File,
				Name:  f.name})
		}
	}
	// 遍历目录
	if d.directories != nil {
		for _, dir := range *d.directories {
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
	fmt.Println("[Create]Dir:", d.name, "Name:", req.Name)
	// 定义文件路径
	path := BackendDir + req.Name       // 压缩后的存放路径
	rawPath := path + ".compressfs.raw" // 解压后的存放路径
	// 创建文件
	fc, err := os.Create(BackendDir + req.Name)
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
	f := &File{
		Node: Node{
			name:  req.Name,
			inode: NewInode()},
		rawPath:  rawPath,
		modified: false,
		file:     fc2,
		flag:     os.O_WRONLY | os.O_CREATE}
	// 把文件加到目录的文件列表里
	files := []*File{f}
	if d.files != nil {
		files = append(files, *d.files...)
	}
	d.files = &files
	// 返回Node
	return f, f, nil
}

// 删除文件或目录 https://godoc.org/bazil.org/fuse#RemoveRequest
func (d *Dir) Remove(ctx context.Context, req *fuse.RemoveRequest) error {
	fmt.Println("[Remove]Dir:", d.name, "Name:", req.Name, "Dir:", req.Dir)
	os.Remove(BackendDir + req.Name) // 删除文件或空目录
	// 如果是删除目录，判断是不是有目录
	if req.Dir && d.directories != nil {
		newDirs := []*Dir{}
		// 如果有，遍历目录名，组成一个新的目录列表
		for _, dir := range *d.directories {
			if dir.name != req.Name {
				newDirs = append(newDirs, dir)
			}
		}
		d.directories = &newDirs
		return nil
		// 如果删除文件，先看有没有文件
	} else if !req.Dir && *d.files != nil {
		newFiles := []*File{}
		// 如果有，遍历文件名，组成一个新的文件列表
		for _, f := range *d.files {
			if f.name != req.Name {
				newFiles = append(newFiles, f)
			}
		}
		d.files = &newFiles
		return nil
	}
	// 返回 ENOENT
	return fuse.ENOENT
}

// 创建目录 https://godoc.org/bazil.org/fuse/fs#NodeMkdirer
func (d *Dir) Mkdir(ctx context.Context, req *fuse.MkdirRequest) (fs.Node, error) {
	fmt.Println("[Mkdir]", d.name, "Name:", req.Name, "Mode:", req.Mode)
	path := BackendDir + req.Name
	// 创建目录
	os.Mkdir(path, req.Mode)
	// 构造一个目录结构体
	f := &Dir{
		Node: Node{
			name:  req.Name,
			inode: NewInode()},
		files:       &[]*File{},
		directories: &[]*Dir{}}
	// 把新建的目录加到目录的目录列表里
	dirs := []*Dir{f}
	if d.directories != nil {
		dirs = append(dirs, *d.directories...)
	}
	d.directories = &dirs
	return f, nil
}

// 打开文件 https://godoc.org/bazil.org/fuse/fs#NodeOpener
func (f *File) Open(ctx context.Context, req *fuse.OpenRequest, resp *fuse.OpenResponse) (fs.Handle, error) {
	fmt.Println("[Open]", f.name, "Inode:", f.inode, "Dir:", req.Dir, "Flags:", req.Flags)
	// 定义路径
	path := BackendDir + f.name
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
		// 如果是RDONLY，则复制一个Node返回，否则把文件指针赋值到原Node的file
		if int(req.Flags) == os.O_RDONLY {
			fr, err := os.OpenFile(rawPath, os.O_RDONLY, 0755)
			if err != nil {
				fmt.Println("[ERROR]打开解压后的文件错误", err)
			}
			// 只读模式，构造一个新的Node返回，从而实现可以多次Open文件进行读取
			fn := &File{
				Node: Node{
					name:  f.name,
					inode: f.inode},
				rawPath: f.rawPath,
				file:    fr,
				flag:    os.O_RDONLY} // os.O_RDONLY == 0 详见：https://godoc.org/syscall#O_RDONLY
			//返回文件Handle
			return fn, nil
		} else {
			// 如果是 O_WRONLY ，则清空文件。
			// 如果不加这个，当写入文件时会出现文件只能增大不能缩小的BUG。
			//   如果对文件进行写入，不添加 O_TRUNC 时的 BUG 现象：
			//   会以 WRONLY 打开文件，之后还会以 RDONLY 打开一次。先读出一堆 \0 ，然后再写入。
			//   如果写入的长度比读出的长度短，还会以 \0 补全。
			//   不知道为啥，怀疑是这个库的 bug 。而且这个库也没提供 truncate 的 Handler 。
			//   最后是通过使用 pyfuse 进行对比测试发现在 Open 和 Write 之前缺少了 truncate 。
			//   加入 truncate 后表现正常，不会打开两次文件。
			flags := int(req.Flags)
			if flags&os.O_WRONLY > 0 {
				flags |= os.O_TRUNC
			}
			fr, err := os.OpenFile(rawPath, flags, 0755)
			if err != nil {
				fmt.Println("[ERROR]打开解压后的文件错误", err)
			}
			f.file = fr
			f.flag = int(req.Flags)
		}
	}
	//返回文件Handle
	return f, nil
}

// 读取文件 https://godoc.org/bazil.org/fuse/fs#HandleReader
func (f *File) Read(ctx context.Context, req *fuse.ReadRequest, resp *fuse.ReadResponse) error {
	fmt.Println("[Read]", f.name, "Inode:", f.inode, "Dir:", req.Dir, "Size:", req.Size, "Offset:", req.Offset)
	// 读取解压后的文件，赋值到 resp.Data
	f.file.ReadAt(resp.Data[:req.Size], req.Offset)
	// 调整切片长度，详见切片机制：https://blog.csdn.net/u013474436/article/details/88770501
	resp.Data = resp.Data[:req.Size]
	return nil
}

// 写入文件 https://godoc.org/bazil.org/fuse/fs#HandleWriter 【文件变小的时候会有bug】
func (f *File) Write(ctx context.Context, req *fuse.WriteRequest, resp *fuse.WriteResponse) error {
	resp.Size = len(req.Data)
	fmt.Println("[Write]", f.name, "Inode:", f.inode, "Size:", resp.Size, "Offset:", req.Offset, "Flags:", req.Flags, "FileFlags:", req.FileFlags)
	// 如果flag是只读，则返回错误
	if f.flag == os.O_RDONLY {
		return fuse.EPERM // EPERM 操作不允许 参考：https://godoc.org/bazil.org/fuse#pkg-constants https://blog.csdn.net/a8039974/article/details/25830705
	}
	// 文件标记为被修改
	f.modified = true
	// 写入文件
	fmt.Println(req.Data)
	n, err := f.file.WriteAt(req.Data, req.Offset) // BUG: 文件大小可扩不可缩 写入的数据总是一堆0（py试试？）通过python的fuse测试，发现go的fuse缺少truncate操作
	fmt.Println("Write", n, "bytes")
	if err != nil {
		fmt.Println("[ERROR]写入文件错误", err)
	}
	return nil
}

// 释放文件 https://godoc.org/bazil.org/fuse/fs#HandleReleaser
func (f *File) Release(ctx context.Context, req *fuse.ReleaseRequest) error {
	fmt.Println("[Release]", f.name, "Inode:", f.inode)
	// 如果是只读，直接返回
	if f.flag == os.O_RDONLY {
		f.file.Close()
		return nil
	}
	// 定义路径变量
	path := BackendDir + f.name
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
	// 删除解压后的文件
	os.Remove(f.rawPath)
	f.rawPath = ""
	return nil
}

// 同步文件修改到磁盘 https://godoc.org/bazil.org/fuse/fs#HandleFlusher
func (f *File) Flush(ctx context.Context, req *fuse.FlushRequest) error {
	fmt.Println("[Flush]", f.name)
	f.file.Sync()
	return nil
}

// fsync（也是同步到磁盘） https://godoc.org/bazil.org/fuse/fs#NodeFsyncer
func (f *File) Fsync(ctx context.Context, req *fuse.FsyncRequest) error {
	fmt.Println("[Fsync]", f.name)
	f.file.Sync()
	return nil
}

// 重命名 https://godoc.org/bazil.org/fuse/fs#NodeRenamer
// TODO: BUG https://github.com/bazil/fuse/blob/master/fs/serve.go#L1314
// func (f *File) Rename(ctx context.Context, req *fuse.RenameRequest, newDir Node) error {
// 	fmt.Println("[Rename]", f.name, req, newDir)
// 	return nil
// }
// func (d *Dir) Rename(ctx context.Context, req *fuse.RenameRequest, newDir Node) error {
// 	fmt.Println("[Rename]", d.name, req, newDir)
// 	return nil
// }