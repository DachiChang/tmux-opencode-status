// Command pack archives a single executable in a reproducible .tar.gz.
// It fixes tar permissions, ownership, and timestamps and omits the gzip
// filename and timestamp so identical inputs produce the same SHA-256 hash.
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
	in := flag.String("in", "", "executable to package")
	out := flag.String("out", "", "output .tar.gz path")
	name := flag.String("name", "tmux-opencode-status", "filename inside the archive")
	flag.Parse()
	if *in == "" || *out == "" {
		fmt.Fprintln(os.Stderr, "Usage: pack -in <executable> -out <archive.tar.gz> [-name <filename>]")
		os.Exit(2)
	}
	if err := pack(*in, *out, *name); err != nil {
		fmt.Fprintln(os.Stderr, "pack:", err)
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
	gz.Header = gzip.Header{} // Omit the filename and timestamp.
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
