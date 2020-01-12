// 一个基于FUSE的压缩文件系统。
//
// 目前暂不支持目录和权限修改。
//
// TODO：性能优化：read和write不打开文件，file属性里面存一个*os.File；捕获退出信号，自动umount
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
)

// 存放压缩文件的后端目录
var BackendDir string

// 压缩方式
var CompressType string

// 挂载目录
var Mountpoint string

// 帮助信息
const HELP_INFO = `
支持的压缩方式有：lzw,flate1,flate9,gzip,zlib。
	lzw: lzw的方式压缩
	flate1: flate的方式，最快速度
	flate9: flate的方式，最高压缩率
	gzip: gzip的方式，最高压缩率（待测试）
	zlib: zlib的方式，最高压缩率（待测试）

注意：一旦backend里面有文件，下次重新挂载的时候，压缩方式不能修改！
`

func usage() {
	fmt.Fprintf(os.Stderr, "Usage of %s:\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "  %s BackendDir Mountpoint CompressType\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "例子：  %s /tmp/backend/ /mnt lzw\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "注意BackendDir后面要有斜杠。Mountpoint有无都可\n")
	fmt.Fprintf(os.Stderr, HELP_INFO)
}

func main() {

	flag.Usage = usage
	flag.Parse()

	if flag.NArg() < 3 {
		usage()
		os.Exit(2)
	}
	BackendDir = flag.Arg(0)
	Mountpoint = flag.Arg(1)
	CompressType = flag.Arg(2)
	switch CompressType {
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

	if err := run(); err != nil {
		log.Fatal(err)
	}
}
