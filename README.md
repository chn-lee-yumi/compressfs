# compressfs

- 一个简陋的压缩文件系统，基于bazil.org/fuse。说是简陋，因为只实现了读写删三个功能。不支持目录，不支持修改权限。
- 写这个的目的是为了验证自己的想法能否实现。据我所知，目前Linux文件系统里面很少有支持压缩的文件系统，btrfs算一个。Windows下面有NTFS支持压缩。所以我决定采用Go语言来写一个FUSE。
- 实验是成功的。性能测试如下。(因测试时间较长，本人时间有限，未进行多次测试，结果仅供参考！)

```shell
sysbench --test=fileio --file-total-size=5G prepare
sysbench --test=fileio --file-total-size=5G --file-test-mode=seqwr run # (顺序写)
sysbench --test=fileio --file-total-size=5G --file-test-mode=seqrd run # (顺序读)
sysbench --test=fileio --file-total-size=5G --file-test-mode=rndrw --max-time=300 --max-requests=0 run # (随机读写)
sysbench --test=fileio --file-total-size=5G cleanup # (清理测试文件)

apfs：
顺序读：3900.77 MiB/s
顺序写：653.31 MiB/s
随机读：277.34 MiB/s
随机写：184.89 MiB/s
compressfs：(lzw)
顺序读：659.93 MiB/s
顺序写：173.95 MiB/s
随机读：74.52 MiB/s
随机写：49.68 MiB/s
compressfs：(flate,BestSpeed)
顺序读：648.84 MiB/s
顺序写：152.84 MiB/s
随机读：64.08 MiB/s
随机写：42.72 MiB/s
compressfs：(flate,BestCompression) # 这个比BestSpeed还要高，可能是误差
顺序读：650.15 MiB/s
顺序写：161.19 MiB/s
随机读：66.87 MiB/s
随机写：44.58 MiB/s
```

- 目前支持两种压缩方式：lzw和flate。压缩率：flate > lzw。

## 使用方法

- 安装依赖`bazil.org/fuse`
- 下载源码，build。
- 新建一个文件夹，例如testdir。这个文件夹用于存放压缩后的文件。
- 新建一个文件夹，用于挂载compressfs。也可以直接挂载到/mnt。
- ./compressfs ./testdir /mnt lzw
- 挂载成功后，可以往/mnt里拷贝几个文件，然后可以在testdir里面看到压缩后到文件，用`ls -l`命令可以对比文件大小。
- 你可以拷个可执行文件到/mnt，然后运行，发现是可以正常运行的。
