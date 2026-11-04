package exttest

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// HelperArg is the argument a test binary's TestMain looks for to run as
// the execution checks' helper: given it first, the binary runs RunHelper
// on the arguments after it and exits with its answer, so a RuntimeCase's
// Argv is the test binary and HelperArg (Go's helper-process pattern).
const HelperArg = "exttest-helper"

// HelperDirFile is the file the checks write in the directory a helper is
// started in, holding a word no other directory holds: a helper reads it
// from its working directory, which tells the checks it started there
// (mounted or copied, wherever the Runtime runs it).
const HelperDirFile = "exttest-dir"

// RunHelper is the program the execution checks drive (RuntimeExec), its
// mode and that mode's arguments in args, answering the exit code the
// program exits with. What it prints are lines beginning "exttest-"; on a
// terminal each ends in "\r\n". The modes:
//
//   - report ARG...: prints "exttest-dir:" and what HelperDirFile in its
//     working directory holds, "exttest-env:" and its environment, sorted,
//     as a JSON array, and "exttest-args:" and ARG... as a JSON array;
//     exits 0.
//   - terminal ARG...: what report prints, then "exttest-size:ROWS COLS",
//     its terminal's size, and "exttest-ready"; prints the size again at
//     each change of it (SIGWINCH); reads one line of input, prints
//     "exttest-got:" and the line, and exits 3.
//   - asked: prints "exttest-ready" and runs until asked to end (SIGTERM),
//     then exits 0.
//   - ignore: prints "exttest-ready", and "exttest-asked" at each ask to
//     end (SIGTERM), which it ignores: it runs until it is forced.
//   - hold: prints "exttest-ready" and runs until it is ended, the ask
//     ending it as it ends any program that does not handle it.
//   - exit N: exits N at once.
//   - spew N: prints N bytes, the i-th of them 'a'+i%26, and exits 0.
//   - http PORT: listens on 127.0.0.1:PORT, prints "exttest-listening",
//     and serves until it is ended: each request printed as
//     "exttest-request:" and its path and query, then answered, /redirect
//     with a 302 to /ok, /ok and /last with a 204, anything else a 404.
//
// A mode it does not know, or arguments it cannot read, is exit 2 with why
// on its standard error.
func RunHelper(args []string) int {
	if len(args) == 0 {
		return helperRefuses("no mode")
	}
	mode, rest := args[0], args[1:]
	switch mode {
	case "report":
		helperReport(rest)
		return 0
	case "terminal":
		return helperTerminal(rest)
	case "asked":
		asks := make(chan os.Signal, 1)
		signal.Notify(asks, syscall.SIGTERM)
		fmt.Println("exttest-ready")
		<-asks
		return 0
	case "ignore":
		asks := make(chan os.Signal, 1)
		signal.Notify(asks, syscall.SIGTERM)
		fmt.Println("exttest-ready")
		for range asks {
			fmt.Println("exttest-asked")
		}
		return 0
	case "hold":
		fmt.Println("exttest-ready")
		for {
			time.Sleep(time.Hour)
		}
	case "exit":
		if len(rest) != 1 {
			return helperRefuses("exit takes one number")
		}
		n, err := strconv.Atoi(rest[0])
		if err != nil {
			return helperRefuses("exit: " + err.Error())
		}
		return n
	case "spew":
		if len(rest) != 1 {
			return helperRefuses("spew takes one number")
		}
		n, err := strconv.Atoi(rest[0])
		if err != nil || n < 0 {
			return helperRefuses("spew: not a count: " + rest[0])
		}
		w := bufio.NewWriter(os.Stdout)
		for i := range n {
			w.WriteByte(Spewed(i))
		}
		if err := w.Flush(); err != nil {
			return helperRefuses("spew: " + err.Error())
		}
		return 0
	case "http":
		if len(rest) != 1 {
			return helperRefuses("http takes one port")
		}
		return helperHTTP(rest[0])
	}
	return helperRefuses("no mode " + strconv.Quote(mode))
}

// Spewed is the i-th byte the helper's spew mode prints.
func Spewed(i int) byte { return byte('a' + i%26) }

func helperRefuses(why string) int {
	fmt.Fprintln(os.Stderr, "exttest helper: "+why)
	return 2
}

// helperReport prints where the helper runs, its environment and args.
func helperReport(args []string) {
	if b, err := os.ReadFile(HelperDirFile); err != nil {
		fmt.Println("exttest-dir-error:" + err.Error())
	} else {
		fmt.Println("exttest-dir:" + strings.TrimSpace(string(b)))
	}
	env := slices.Sorted(slices.Values(os.Environ()))
	fmt.Println("exttest-env:" + jsonLine(env))
	if args == nil {
		args = []string{}
	}
	fmt.Println("exttest-args:" + jsonLine(args))
}

func jsonLine(v []string) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// helperTerminal is the terminal mode: the report, the size, a resize
// told at each change, one line read and exit 3.
func helperTerminal(args []string) int {
	resized := make(chan os.Signal, 1)
	notifyResize(resized)
	helperReport(args)
	size := func() {
		if rows, cols, err := terminalSize(); err != nil {
			fmt.Println("exttest-size-error:" + err.Error())
		} else {
			fmt.Printf("exttest-size:%d %d\n", rows, cols)
		}
	}
	size()
	lines := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(os.Stdin)
		if sc.Scan() {
			lines <- strings.TrimRight(sc.Text(), "\r")
		}
		close(lines)
	}()
	fmt.Println("exttest-ready")
	for {
		select {
		case <-resized:
			size()
		case l, ok := <-lines:
			if !ok {
				return helperRefuses("terminal: no line on its input")
			}
			fmt.Println("exttest-got:" + l)
			return 3
		}
	}
}

// helperHTTP is the http mode.
func helperHTTP(port string) int {
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return helperRefuses("http: not a port: " + port)
	}
	l, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", port))
	if err != nil {
		return helperRefuses("http: " + err.Error())
	}
	fmt.Println("exttest-listening")
	srv := &http.Server{ReadHeaderTimeout: time.Minute, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Println("exttest-request:" + r.URL.RequestURI())
		switch r.URL.Path {
		case "/redirect":
			http.Redirect(w, r, "/ok", http.StatusFound)
		case "/ok", "/last":
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	})}
	return helperRefuses("http: " + srv.Serve(l).Error())
}
