// Command mkinitrd packs one static binary as /init into a gzipped newc
// initramfs, with the /dev/console node the kernel needs for PID 1 output.
package main

import (
	"compress/gzip"
	"fmt"
	"io"
	"os"
)

type entry struct {
	name  string
	mode  uint32
	major uint32
	minor uint32
	data  []byte
}

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: mkinitrd <static-binary> <out.cpio.gz>")
		os.Exit(2)
	}
	bin, err := os.ReadFile(os.Args[1])
	check(err)
	out, err := os.Create(os.Args[2])
	check(err)
	gz := gzip.NewWriter(out)
	entries := []entry{
		{name: "dev", mode: 0o040755},
		{name: "dev/console", mode: 0o020600, major: 5, minor: 1},
		{name: "proc", mode: 0o040755},
		{name: "init", mode: 0o100755, data: bin},
		{name: "TRAILER!!!"},
	}
	for i, e := range entries {
		writeEntry(gz, uint32(i+1), e)
	}
	check(gz.Close())
	check(out.Close())
}

func writeEntry(w io.Writer, ino uint32, e entry) {
	name := e.name + "\x00"
	hdr := fmt.Sprintf("070701%08X%08X%08X%08X%08X%08X%08X%08X%08X%08X%08X%08X%08X",
		ino, e.mode, 0, 0, 1, 0, len(e.data), 0, 0, e.major, e.minor, len(name), 0)
	buf := []byte(hdr + name)
	buf = append(buf, make([]byte, pad4(len(buf)))...)
	buf = append(buf, e.data...)
	buf = append(buf, make([]byte, pad4(len(e.data)))...)
	_, err := w.Write(buf)
	check(err)
}

func pad4(n int) int { return (4 - n%4) % 4 }

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
