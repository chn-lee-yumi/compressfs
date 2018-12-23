// 目前暂不支持目录和权限修改。
// TODO:读写效率极低，优化方案：打开文件时解压(open/create)，关闭文件时压缩(release)。
// TODO:gzip、zlib有BUG

package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	//"syscall"
	"io/ioutil"
	"bazil.org/fuse"
	"bazil.org/fuse/fs"
	_ "bazil.org/fuse/fs/fstestutil"
	"bazil.org/fuse/fuseutil"
	"golang.org/x/net/context"
	"io"
	"bytes"
	"compress/lzw"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
)

var SOURCE_DIR string
var compress_mode string

var inode uint64

func NewInode() uint64 {
	inode += 1
	return inode
}

type Node struct {
	inode uint64
	name  string
}

const compress_description=`
支持的压缩方式有：lzw,flate1,flate9。
	lzw: lzw的方式压缩
	flate1: flate的方式，最快速度
	flate9: flate的方式，最高压缩率
`
func usage() {
	fmt.Fprintf(os.Stderr, "Usage of %s:\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "  %s SOURCE_DIR MOUNTPOINT COMPRESS_TYPE\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "例子：  %s testdir/ /mnt lzw\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "注意SOURCE_DIR后面要有斜杠。MOUNTPOINT有无都可\n")
	fmt.Fprintf(os.Stderr, compress_description)
	//flag.PrintDefaults()
}

func run(mountpoint string) error {
	c, err := fuse.Mount(
		mountpoint,
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

	fmt.Println("[run]读取当前目录！")
	var files []*File
	dir, err := ioutil.ReadDir(SOURCE_DIR)//读取目录文件名
	if err!=nil {
		fmt.Println("[Lookup ERROR]目录打开错误！",err)
		os.Exit(1)
	}
	fmt.Println("[run]给files赋值！")
    for _,f := range dir {
        files=append(files,&File{
			Node: Node{inode: NewInode(), name: f.Name()}})
    }
	fmt.Println("[run]给filesys赋值！")
	filesys := &FS{&Dir{
		Node: Node{inode: NewInode(), name: "mount"},
		files: &files,
		directories: &[]*Dir{}}}

	fmt.Println("[run]给srv赋值！")
	srv := fs.New(c, nil)
	fmt.Println("[run]即将调用Serve！")
	if err := srv.Serve(filesys); err != nil {
		return err
	}
	fmt.Println("[run]调用Serve完毕！")

	// Check if the mount process has an error to report.
	<-c.Ready
	if err := c.MountError; err != nil {
		return err
	}
	return nil
}

func main() {
	flag.Usage = usage
	flag.Parse()

	if flag.NArg() < 3 {
		usage()
		os.Exit(2)
	}
	SOURCE_DIR = flag.Arg(0)
	mountpoint := flag.Arg(1)
	compress_mode=flag.Arg(2)
	switch compress_mode {
	case "lzw":
	case "flate1":
	case "flate9":
	case "gzip":
	case "zlib":
	default:
		fmt.Println("压缩参数错误！")
		usage()
		os.Exit(2)
	}

	if err := run(mountpoint); err != nil {
		log.Fatal(err)
	}
}

func (d *Dir) Remove(ctx context.Context, req *fuse.RemoveRequest) error {
	os.Remove(SOURCE_DIR+req.Name)
	if req.Dir && d.directories != nil {
		newDirs := []*Dir{}
		for _, dir := range *d.directories {
			if dir.name != req.Name {
				newDirs = append(newDirs, dir)
			}
		}
		d.directories = &newDirs
		return nil
	} else if !req.Dir && *d.files != nil {
		newFiles := []*File{}
		for _, f := range *d.files {
			if f.name != req.Name {
				newFiles = append(newFiles, f)
			}
		}
		d.files = &newFiles
		return nil
	}
	return fuse.ENOENT
}

/**********************************************/

type FS struct {
	root *Dir
}

var _ fs.FS = (*FS)(nil)

func (f *FS) Root() (fs.Node, error) {
	return f.root, nil
	//return &Dir{fs: f}, nil
}

// Dir implements both Node and Handle for the root directory.
type Dir struct {
	Node
	files       *[]*File
	directories *[]*Dir
}

var _ fs.Node = (*Dir)(nil)

func (d *Dir) Attr(ctx context.Context, a *fuse.Attr) error {
	a.Inode = 1
	a.Mode = os.ModeDir | 0555
	//fmt.Println("[*Dir Attr]ctx:",ctx) //好像没什么用
	//fmt.Println("[*Dir Attr]a:",a) //文件属性
	return nil
}

var _ fs.NodeStringLookuper = (*Dir)(nil)

func (d *Dir) Lookup(ctx context.Context, name string) (fs.Node, error) {
	fmt.Println("[Lookup]name:",name)

	if d.files != nil {
		for _, n := range *d.files {
			if n.name == name {
				return n, nil
			}
		}
	}
	if d.directories != nil {
		for _, n := range *d.directories {
			if n.name == name {
				return n, nil
			}
		}
	}
	return nil, fuse.ENOENT
}

var _ fs.HandleReadDirAller = (*Dir)(nil)

func (d *Dir) ReadDirAll(ctx context.Context) ([]fuse.Dirent, error) {
	var children []fuse.Dirent
	if d.files != nil {
		for _, f := range *d.files {
			children = append(children, fuse.Dirent{Inode: f.inode, Type: fuse.DT_File, Name: f.name})
		}
	}
	if d.directories != nil {
		for _, dir := range *d.directories {
			children = append(children, fuse.Dirent{Inode: dir.inode, Type: fuse.DT_Dir, Name: dir.name})
		}
	}
	return children, nil
}

func (f *File) Write(ctx context.Context, req *fuse.WriteRequest, resp *fuse.WriteResponse) error {
	fmt.Println("[Write]req:",req)
	resp.Size = len(req.Data)
	//先解压文件
	fr, err := os.Open(SOURCE_DIR+f.name)
	defer fr.Close()
	if err!=nil {
		fmt.Println("[Write ERROR]",err)
	}
	ft, err := os.OpenFile(SOURCE_DIR+f.name+".tmp", os.O_RDWR|os.O_CREATE, 0755)
	defer os.Remove(SOURCE_DIR+f.name+".tmp")
	defer ft.Close()
	if err!=nil {
		fmt.Println("[Write ERROR]",err)
	}
	r,err := NewReader(fr)
	if err != nil {
		fmt.Println(err.Error())
		return nil
	}
	defer r.Close()
 	//读取压缩文件
	io.Copy(ft, r)
	//修改内容
	ft.WriteAt(req.Data,req.Offset)
	//写入到文件
	fw, err := os.OpenFile(SOURCE_DIR+f.name, os.O_WRONLY|os.O_TRUNC, 0600)
	defer fw.Close()
	if err != nil {
		fmt.Println("[Write ERROR]打开文件失败！",f.name,err.Error())
		return nil
	}
	w,err := NewWriter(fw)
	if err != nil {
		fmt.Println(err.Error())
		return nil
	}
    defer w.Close()
 	//压缩方式写入
	ft.Seek(0,os.SEEK_SET)
    io.Copy(w, ft)
	/*_, err = fw.Write(req.Data)
	if err != nil {
		fmt.Println("[Write ERROR]写入文件失败！",f.name,err.Error())
	}*/

	return nil
}

func (d *Dir) Create(ctx context.Context, req *fuse.CreateRequest, resp *fuse.CreateResponse) (fs.Node, fs.Handle, error) {
	fmt.Println("[Create]req:",req)
	f := &File{Node: Node{name: req.Name, inode: NewInode()}}
	files := []*File{f}
	if d.files != nil {
		files = append(files, *d.files...)
	}
	d.files = &files
	//创建文件
	fc,err := os.Create(SOURCE_DIR+req.Name)
	defer fc.Close()
	if err!=nil {
		fmt.Println("[Create ERROR]创建文件失败！",err.Error())
	}

	return f, f, nil
}

/**********************************************/

type File struct {
	Node
	tmpPath string //如果为空，说明没有解压。解压后这里设置为解压后的路径。release时再压缩写入。
	modified bool //如果为true，release后压缩写入，否则不进行操作
}

var _ fs.Node = (*File)(nil)

func (f *File) Attr(ctx context.Context, a *fuse.Attr) error { //该函数返回文件属性
	//TODO：增加access time，根据访问时间进行缓存的删除
	a.Inode = f.inode
	a.Mode = 0777 //TODO:增加chmod特性
	fmt.Println("[*File Attr]f.name:",f.name)
	a.Size = getFileSize(SOURCE_DIR+f.name)
	fmt.Println("[*File Attr]a:",a) //文件属性
	return nil
}

var _ fs.NodeOpener = (*File)(nil)

func (f *File) Open(ctx context.Context, req *fuse.OpenRequest, resp *fuse.OpenResponse) (fs.Handle, error) {
	fmt.Println("[Open]file:",f.name)
	return f, nil
}

var _ fs.Handle = (*File)(nil)

var _ fs.HandleReader = (*File)(nil)

func (f *File) Read(ctx context.Context, req *fuse.ReadRequest, resp *fuse.ReadResponse) error {
	fmt.Println("[Read]file:",f.name)
	fr,err:=os.Open(SOURCE_DIR+f.name)
    defer fr.Close()
	if err!=nil {
		fmt.Println("[Read ERROR]",err)
	}
	r,err := NewReader(fr)
	if err != nil {
		fmt.Println(err.Error())
		return nil
	}
    defer r.Close()
 	//读取压缩文件
	buf:=bytes.NewBuffer(nil)
    io.Copy(buf, r)

	fuseutil.HandleRead(req, resp, buf.Bytes())
	//fmt.Println("[Read]ctx:",ctx) //好像没什么用
	fmt.Println("[Read]req:",req)
	fmt.Println("[Read]resp:",resp)
	return nil
}

func (f *File) Fsync(ctx context.Context, req *fuse.FsyncRequest) error {
	fmt.Println("[Fsync]file:",f.name)
	return nil
}

func (f *File) Release(ctx context.Context, req *fuse.ReleaseRequest) error {
	fmt.Println("[Release]file:",f.name)
	/*type ReleaseRequest struct {
    Header       `json:"-"`
    Dir          bool // is this Releasedir?
    Handle       HandleID
    Flags        OpenFlags // flags from OpenRequest
    ReleaseFlags ReleaseFlags
    LockOwner    uint32
	}*/
	return nil
}

func (f *File) Flush(ctx context.Context, req *fuse.FlushRequest) error {
	fmt.Println("[Flush]file:",f.name)
	/*type FlushRequest struct {
    Header    `json:"-"`
    Handle    HandleID
    Flags     uint32
    LockOwner uint64
	}*/
	return nil
}

/**********************************************/

func getFileSize(filepath string) uint64 {
	fr,err:=os.Open(filepath)
	defer fr.Close()
	if err!=nil {
		fmt.Println("[getFileSize ERROR, os.Open]",err)
		return uint64(0)
	}
	r,err := NewReader(fr)
	if err != nil {
		fmt.Println("[getFileSize ERROR, NewReader]",err)
		return uint64(0)
	}
	defer r.Close()
	//读取压缩文件
	buf:=bytes.NewBuffer(nil)
    io.Copy(buf, r)
	return uint64(buf.Len())
    /*f, err := os.Open(filepath)
    if err != nil {
        fmt.Println("[getFileSize ERROR]文件大小读取错误！",err)
        return uint64(0)
    }
    file_size, err := f.Seek(0, os.SEEK_END)
	if err!=nil {
		fmt.Println("[getFileSize ERROR]Seek发生错误！",err)
		os.Exit(1)
	}
    return uint64(file_size)*/
}

func NewReader(r io.Reader)(io.ReadCloser,error){
	switch compress_mode {
	case "lzw":
		return lzw.NewReader(r, lzw.LSB, 8),nil
	case "flate1":
		return flate.NewReader(r),nil
	case "flate9":
		return flate.NewReader(r),nil
	case "gzip":
		return gzip.NewReader(r)
	case "zlib":
		return zlib.NewReader(r)
	}
	fmt.Println("[ERROR]!")
	return nil,nil
}

func NewWriter(w io.Writer)(io.WriteCloser,error){
	switch compress_mode {
	case "lzw":
		return lzw.NewWriter(w, lzw.LSB, 8),nil
	case "flate1":
		return flate.NewWriter(w,flate.BestSpeed)
	case "flate9":
		return flate.NewWriter(w,flate.BestCompression)
	case "gzip":
		return gzip.NewWriterLevel(w,gzip.BestCompression)
	case "zlib":
		return zlib.NewWriterLevel(w,zlib.BestCompression)
	}
	fmt.Println("[ERROR]!")
	return nil,nil
}
