package juju

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"io/ioutil"
	"launchpad.net/juju/go/version"
	"log"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"reflect"
)

// tarHeader returns a file header given the 
func tarHeader(i os.FileInfo) *tar.Header {
	return &tar.Header{
		Typeflag:   tar.TypeReg,
		Name:       i.Name(),
		Size:       i.Size(),
		Mode:       int64(i.Mode() & 0777),
		ModTime:    i.ModTime(),
		AccessTime: i.ModTime(),
		ChangeTime: i.ModTime(),
		Uname:      "ubuntu",
		Gname:      "ubuntu",
	}
}

// isExecutable returns whether the given info
// represents a regular file executable by (at least) the user.
func isExecutable(i os.FileInfo) bool {
	return i.Mode()&(0100|os.ModeType) == 0100
}

// archive writes the executable files found in the given
// directory in gzipped tar format to w.
func archive(w io.Writer, dir string) error {
	entries, err := ioutil.ReadDir(dir)
	if err != nil {
		return err
	}

	gzw := gzip.NewWriter(w)
	tarw := tar.NewWriter(gzw)
	defer tarw.Close()
	for _, ent := range entries {
		if !isExecutable(ent) {
			panic(fmt.Errorf("go install has created non-executable file %q", ent.Name()))
		}
		h := tarHeader(ent)
		// ignore local umask
		h.Mode = 0755
		err := tarw.WriteHeader(h)
		if err != nil {
			return err
		}
		if err := copyFile(tarw, filepath.Join(dir, ent.Name())); err != nil {
			return err
		}
	}
	tarw.Close()
	return gzw.Close()
}

func copyFile(w io.Writer, file string) error {
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(w, f)
	return err
}

var jujuRoot string

func init() {
	// Find out the juju root by introspecting a locally defined type.
	type t int
	p := reflect.TypeOf(t(0)).PkgPath()
	root, j := path.Split(p)
	if j != "juju" {
		panic(fmt.Errorf("unexpected juju package path %q", p))
	}
	jujuRoot = root
}

// bundleTools bundles all the current juju tools in gzipped tar
// format to the given writer.
func bundleTools(w io.Writer) error {
	dir, err := ioutil.TempDir("", "juju-tools")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	cmd := exec.Command("go", "install", path.Join(jujuRoot, "cmd", "..."))
	cmd.Env = []string{
		"GOPATH=" + os.Getenv("GOPATH"),
		"GOBIN=" + dir,
		"PATH=" + os.Getenv("PATH"),
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("build failed: %v; %s", err, out)
	}
	return archive(w, dir)
}

func (c *Conn) UploadTools() error {
	// We create the entire archive
	// before asking the environment to start uploading
	// so that we can be sure we have archived correctly.
	f, err := ioutil.TempFile("", "juju-tgz")
	if err != nil {
		return err
	}
	defer f.Close()
	defer os.Remove(f.Name())
	err = bundleTools(f)
	if err != nil {
		return err
	}
	_, err = f.Seek(0, 0)
	if err != nil {
		return err
	}
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	return c.Environ.UploadTools(f, fi.Size(), version.Current)
}
