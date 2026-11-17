// pack 把單一執行檔封裝成可重現的 .tar.gz：固定權限、擁有者與時間戳，gzip 不帶檔名與時間，
// 因此相同輸入在任何主機上都產生逐位元組相同的封存（供 SHA-256 完整性資料與可重現建置使用）。
package main

import (
	"archive/tar"
	"compress/gzip"
	"flag"
	"fmt"
	"os"
	"time"
)

func main() {
	in := flag.String("in", "", "要封裝的執行檔")
	out := flag.String("out", "", "輸出的 .tar.gz")
	name := flag.String("name", "tmux-opencode-status", "封存內的檔名")
	flag.Parse()
	if *in == "" || *out == "" {
		fmt.Fprintln(os.Stderr, "用法：pack -in <執行檔> -out <.tar.gz> [-name <封存內檔名>]")
		os.Exit(2)
	}
	if err := pack(*in, *out, *name); err != nil {
		fmt.Fprintln(os.Stderr, "pack：", err)
		os.Exit(1)
	}
}

func pack(in, out, name string) error {
	data, err := os.ReadFile(in)
	if err != nil {
		return err
	}
	f, err := os.Create(out)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewWriterLevel(f, gzip.BestCompression)
	if err != nil {
		return err
	}
	gz.Header = gzip.Header{} // 無檔名、無時間
	tw := tar.NewWriter(gz)
	hdr := &tar.Header{Typeflag: tar.TypeReg, Name: name, Mode: 0o755, Size: int64(len(data)),
		ModTime: time.Unix(0, 0), Format: tar.FormatUSTAR}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	if _, err := tw.Write(data); err != nil {
		return err
	}
	if err := tw.Close(); err != nil {
		return err
	}
	if err := gz.Close(); err != nil {
		return err
	}
	return f.Close()
}
