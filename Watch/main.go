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
	"gopkg.in/fsnotify.v1"
)

var path = flag.String("p", ".", "specify the path to watch")

func main() {
	flag.Parse()

	var (
		win *acme.Win
		err error
	)

	err = func() error {
		win, err = acme.New()
		if err != nil {
			return err
		}

		if wd, err := os.Getwd(); err != nil {
			return err
		} else {
			win.Ctl("dumpdir %s", wd)
		}
		win.Ctl("dump %s", strings.Join(os.Args, " "))

		abs, err := filepath.Abs(*path)
		if err != nil {
			return err
		}
		switch info, err := os.Stat(abs); {
		case err != nil:
			return err
		case info.IsDir():
			abs += "/"
		}

		win.Name(abs + "+watch")
		win.Ctl("clean")
		win.Fprintf("tag", "Get ")

		run := make(chan runRequest)
		go events(win, run)
		go runner(win, run)
		return watcher(abs, run)
	}()
	if err != nil {
		win.Ctl("delete")
		log.Fatal(err.Error())
	}
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
func watcher(path string, run chan<- runRequest) error {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}

	if err := watchDeep(w, path); err != nil {
		return err
	}

	done := make(chan bool)
	for {
		select {
		case ev := <-w.Events:
			switch ev.Op {
			case fsnotify.Create:
				if err := watchDeep(w, ev.Name); err != nil {
					return err
				}
			case fsnotify.Remove:
				// watcher must not be removed as it is already gone (automagic)
				if strings.HasPrefix(path, ev.Name) {
					return errors.New("Watch point " + path + " deleted")
				}
			}
			run <- runRequest{time.Now(), done}
			<-done

		case err := <-w.Errors:
			return err
		}
	}
}

// WatchDeep watches a directory and all
// of its subdirectories.  If the path is not
// a directory then watchDeep is a no-op.
func watchDeep(w *fsnotify.Watcher, root string) error {
	if err := w.Add(root); err != nil {
		return err
	}
	return filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		switch {
		case os.IsNotExist(err):
			return nil
		case err != nil:
			return err
		case !info.IsDir():
			return nil
		case info.Name() == ".git", info.Name() == "Godeps":
			return filepath.SkipDir
		default:
			return w.Add(path)
		}
	})
}

// Runner runs the commond upon
// receiving an up-to-date runRequest.
func runner(win *acme.Win, reqs <-chan runRequest) {
	runCommand(win)
	last := time.Now()

	for req := range reqs {
		if last.Before(req.time) {
			runCommand(win)
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
func runCommand(win *acme.Win) {
	err := func() error {
		args := flag.Args()
		if len(args) == 0 {
			return errors.New("Must supply a command")
		}
		cmdStr := strings.Join(args, " ")

		win.Addr(",")
		win.Write("data", nil)
		win.Ctl("clean")
		win.Fprintf("body", "$ %s\n", cmdStr)

		cmd := exec.Command(args[0], args[1:]...)
		r, w, err := os.Pipe()
		if err != nil {
			return err
		}
		defer r.Close()
		cmd.Stdout = w
		cmd.Stderr = w

		if err := cmd.Start(); err != nil {
			win.Fprintf("body", "%s: %s\n", cmdStr, err)
			return err
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
		return nil
	}()
	if err != nil {
		win.Ctl("delete")
		log.Fatal(err.Error())
	}
}

// Events handles events coming from the
// acme window.
func events(win *acme.Win, run chan<- runRequest) {
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
