package main

import (
	"compress/flate"
	"compress/gzip"
	"compress/lzw"
	"compress/zlib"
	"fmt"
	"io"
	"os"
)

// 获取文件大小
func getFileSize(filepath string) uint64 {
	f, err := os.Open(filepath)
	if err != nil {
		fmt.Println("[getFileSize ERROR]文件大小读取错误！", err)
		return uint64(0)
	}
	file_size, err := f.Seek(0, os.SEEK_END)
	if err != nil {
		fmt.Println("[getFileSize ERROR]Seek发生错误！", err)
		os.Exit(1)
	}
	return uint64(file_size)
}

// 根据 CompressType ，返回对应的 Reader
func NewReader(r io.Reader) (io.ReadCloser, error) {
	// 注意：lzw.NewReader、flate.NewReader不返回error，所以这里添加了nil
	switch CompressType {
	case "lzw":
		return lzw.NewReader(r, lzw.LSB, 8), nil
	case "flate1":
		return flate.NewReader(r), nil
	case "flate9":
		return flate.NewReader(r), nil
	case "gzip":
		return gzip.NewReader(r)
	case "zlib":
		return zlib.NewReader(r)
	}
	return nil, nil // 正常不应该执行到这里
}

// 根据 CompressType ，返回对应的 Writer
func NewWriter(w io.Writer) (io.WriteCloser, error) {
	// 注意：lzw.NewWriter不返回error，所以这里添加了nil
	switch CompressType {
	case "lzw":
		return lzw.NewWriter(w, lzw.LSB, 8), nil
	case "flate1":
		return flate.NewWriter(w, flate.BestSpeed)
	case "flate9":
		return flate.NewWriter(w, flate.BestCompression)
	case "gzip":
		return gzip.NewWriterLevel(w, gzip.BestCompression)
	case "zlib":
		return zlib.NewWriterLevel(w, zlib.BestCompression)
	}
	return nil, nil // 正常不应该执行到这里
}
