// More portable implementation of
// code.google.com/p/rsc/cmd/Watch.
package main

import (
	"errors"
	"flag"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"9fans.net/go/acme"
	"github.com/howeyc/fsnotify"
)

var path = flag.String("p", ".", "specify the path to watch")

// Win is the acme window.
var win *acme.Win

func main() {
	flag.Parse()

	var err error
	win, err = acme.New()
	if err != nil {
		die(err)
	}

	if wd, err := os.Getwd(); err != nil {
		log.Println(err)
	} else {
		win.Ctl("dumpdir %s", wd)
	}
	win.Ctl("dump %s", strings.Join(os.Args, " "))

	abs, err := filepath.Abs(*path)
	if err != nil {
		die(err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		die(err)
	}
	if info.IsDir() {
		abs += "/"
	}

	win.Name(abs + "+watch")
	win.Ctl("clean")
	win.Fprintf("tag", "Get ")

	run := make(chan runRequest)
	go events(run)
	go runner(run)
	watcher(*path, run)
}

// A runRequests is sent to the runner to request
// that the command be re-run.
type runRequest struct {
	// Time is the times for the request.  This
	// is either the modification time of a
	// changed file, or the time at which a
	// Get event was sent to acme.
	time time.Time

	// Done is a channel upon which the runner
	// should signal its completion.
	done chan<- bool
}

// Watcher watches the directory and sends a
// runRequest when the watched path changes.
func watcher(path string, run chan<- runRequest) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		die(err)
	}

	info, err := os.Stat(path)
	if err != nil {
		die(err)
	}
	if !info.IsDir() {
		// watchDeep is a no-op on regular files.
		if err := w.Watch(path); err != nil {
			die(err)
		}
	} else {
		watchDeep(w, path)
	}

	done := make(chan bool)
	for {
		select {
		case ev := <-w.Event:
			if ev.IsCreate() {
				watchDeep(w, ev.Name)
			}

			info, err := os.Stat(ev.Name)
			for os.IsNotExist(err) {
				dir, _ := filepath.Split(ev.Name)
				if dir == "" {
					break
				}
				info, err = os.Stat(dir)
				if dir == path && os.IsNotExist(err) {
					die(errors.New("Watch point " + path + " deleted"))
				}
			}
			if err != nil {
				die(err)
			}
			run <- runRequest{info.ModTime(), done}
			<-done

		case err := <-w.Error:
			die(err)
		}
	}
}

// WatchDeep watches a directory and all
// of its subdirectories.  If the path is not
// a directory then watchDeep is a no-op.
func watchDeep(w *fsnotify.Watcher, path string) {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		// This file disapeared on us, fine.
		return
	}
	if err != nil {
		die(err)
	}
	if !info.IsDir() {
		return
	}

	switch info.Name() {
	case ".git", "Godep": // ignore
		return
	}

	if err := w.Watch(path); err != nil {
		die(err)
	}

	f, err := os.Open(path)
	if err != nil {
		die(err)
	}
	ents, err := f.Readdirnames(-1)
	if err != nil {
		die(err)
	}
	f.Close()

	for _, e := range ents {
		watchDeep(w, filepath.Join(path, e))
	}
}

// Runner runs the commond upon
// receiving an up-to-date runRequest.
func runner(reqs <-chan runRequest) {
	runCommand()
	last := time.Now()

	for req := range reqs {
		if last.Before(req.time) {
			runCommand()
			last = time.Now()
		}
		req.done <- true
	}
}

// BodyWriter implements io.Writer, writing
// to the body of an acme window.
type BodyWriter struct {
	*acme.Win
}

func (b BodyWriter) Write(data []byte) (int, error) {
	// Don't write too much at once, or else acme
	// can crash…
	sz := len(data)
	for len(data) > 0 {
		n := 1024
		if len(data) < n {
			n = len(data)
		}
		m, err := b.Win.Write("body", data[:n])
		if err != nil {
			return m, err
		}
		data = data[m:]
	}
	return sz, nil
}

// RunCommand runs the command and sends
// the result to the given acme window.
func runCommand() {
	args := flag.Args()
	if len(args) == 0 {
		die(errors.New("Must supply a command"))
	}
	cmdStr := strings.Join(args, " ")

	win.Addr(",")
	win.Write("data", nil)
	win.Ctl("clean")
	win.Fprintf("body", "$ %s\n", cmdStr)

	cmd := exec.Command(args[0], args[1:]...)
	r, w, err := os.Pipe()
	if err != nil {
		die(err)
	}
	defer r.Close()
	cmd.Stdout = w
	cmd.Stderr = w

	if err := cmd.Start(); err != nil {
		win.Fprintf("body", "%s: %s\n", cmdStr, err)
		return
	}
	w.Close()
	io.Copy(BodyWriter{win}, r)
	if err := cmd.Wait(); err != nil {
		win.Fprintf("body", "%s: %s\n", cmdStr, err)
	}

	win.Fprintf("body", "%s\n", time.Now())
	win.Fprintf("addr", "#0")
	win.Ctl("dot=addr")
	win.Ctl("show")
	win.Ctl("clean")
}

// Events handles events coming from the
// acme window.
func events(run chan<- runRequest) {
	done := make(chan bool)
	for e := range win.EventChan() {
		switch e.C2 {
		case 'x', 'X': // execute
			if string(e.Text) == "Get" {
				run <- runRequest{time.Now(), done}
				<-done
				continue
			}
			if string(e.Text) == "Del" {
				win.Ctl("delete")
			}
		}
		win.WriteEvent(e)
	}
	os.Exit(0)
}

// Die closes the acme window and prints an error.
func die(err error) {
	win.Ctl("delete")
	log.Fatal(err.Error())
}
