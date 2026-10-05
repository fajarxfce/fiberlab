package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"ftthlab/internal/app"
	"ftthlab/internal/engine"
	"ftthlab/internal/images"
)

func main() {
	syscall.Umask(0077)
	if err := run(os.Args[1:]); err != nil {
		log.Printf("%v", err)
		os.Exit(1)
	}
}
func run(args []string) error {
	command := "serve"
	if len(args) > 0 {
		command = args[0]
		args = args[1:]
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	switch command {
	case "serve":
		f := flag.NewFlagSet("serve", flag.ContinueOnError)
		dir := f.String("data-dir", ".data", "Local persistent application directory")
		address := f.String("listen", "127.0.0.1:8787", "Loopback HTTP address")
		socket := f.String("socket", "", "Network helper socket (defaults to DATA/netd.sock)")
		dev := f.Bool("dev", false, "Allow the local Vite development origin")
		if err := f.Parse(args); err != nil {
			return err
		}
		host, _, err := net.SplitHostPort(*address)
		if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
			return fmt.Errorf("the local application must listen on a loopback IP, e.g. 127.0.0.1:8787")
		}
		abs, err := filepath.Abs(*dir)
		if err != nil {
			return err
		}
		if *socket == "" {
			*socket = filepath.Join(abs, "netd.sock")
		}
		application, err := app.New(abs, *address, *socket, *dev)
		if err != nil {
			return err
		}
		defer application.Close()
		server := http.Server{Addr: *address, Handler: application.Handler(), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second}
		go func() {
			<-ctx.Done()
			shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			server.Shutdown(shutdown)
		}()
		fmt.Printf("Fiberlab · http://%s\nData: %s\nNetwork helper: %s\n", *address, abs, *socket)
		err = server.ListenAndServe()
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	case "doctor":
		f := flag.NewFlagSet("doctor", flag.ContinueOnError)
		jsonOutput := f.Bool("json", false, "Print JSON")
		if err := f.Parse(args); err != nil {
			return err
		}
		checks := engine.Doctor()
		if *jsonOutput {
			return json.NewEncoder(os.Stdout).Encode(checks)
		}
		for _, c := range checks {
			status := "OK"
			if !c.OK {
				status = "NEEDED"
			}
			fmt.Printf("%-7s %-22s %s\n", status, c.Name, c.Detail)
		}
		return nil
	case "netd":
		f := flag.NewFlagSet("netd", flag.ContinueOnError)
		dir := f.String("data-dir", ".data", "Application data directory (contains CHR images)")
		socket := f.String("socket", "", "Socket accessible only to the application owner")
		uid := f.Int("uid", defaultOwner(), "UID of the unprivileged application owner")
		runtimeDir := f.String("runtime-dir", "", "Root-managed runtime directory")
		cleanup := f.Bool("cleanup", false, "Recover a recorded stale run and exit")
		if err := f.Parse(args); err != nil {
			return err
		}
		if os.Geteuid() != 0 {
			return fmt.Errorf("run netd with sudo; the application itself stays unprivileged")
		}
		if *uid == 0 {
			return fmt.Errorf("pass --uid with the regular application user's UID")
		}
		owner, err := user.LookupId(strconv.Itoa(*uid))
		if err != nil {
			return err
		}
		gid, err := strconv.Atoi(owner.Gid)
		if err != nil {
			return err
		}
		ids, err := owner.GroupIds()
		if err != nil {
			return err
		}
		groups := []uint32{}
		for _, id := range ids {
			n, err := strconv.Atoi(id)
			if err == nil {
				groups = append(groups, uint32(n))
			}
		}
		abs, err := filepath.Abs(*dir)
		if err != nil {
			return err
		}
		if *socket == "" {
			*socket = filepath.Join(abs, "netd.sock")
		}
		if *runtimeDir == "" {
			*runtimeDir = filepath.Join("/var/lib/fiberlab", strconv.Itoa(*uid))
		}
		e := engine.New(*runtimeDir, filepath.Join(abs, "images"), *uid, gid, groups)
		if *cleanup {
			return e.CleanupStale(ctx)
		}
		fmt.Printf("Fiberlab network helper · owner UID %d\nSocket: %s\nRuntime: %s\n", *uid, *socket, *runtimeDir)
		return engine.ServeDaemon(ctx, *socket, e)
	case "images":
		if len(args) == 0 {
			return fmt.Errorf("usage: ftthlab images list|fetch|import [--version 7.x.y] [--data-dir .data]")
		}
		action := args[0]
		f := flag.NewFlagSet("images "+action, flag.ContinueOnError)
		dir := f.String("data-dir", ".data", "Application data directory")
		version := f.String("version", images.DefaultVersion, "Exact CHR version")
		path := f.String("path", "", "Path to official raw CHR image for import")
		if err := f.Parse(args[1:]); err != nil {
			return err
		}
		abs, _ := filepath.Abs(*dir)
		manager := images.Manager{Dir: filepath.Join(abs, "images")}
		switch action {
		case "list":
			list, err := manager.List()
			if err != nil {
				return err
			}
			return json.NewEncoder(os.Stdout).Encode(list)
		case "fetch":
			var previous int64
			image, err := manager.Fetch(ctx, *version, func(read, total int64) {
				if read-previous > 5<<20 {
					fmt.Fprintf(os.Stderr, "Downloaded %.1f MiB\n", float64(read)/(1<<20))
					previous = read
				}
			})
			if err != nil {
				return err
			}
			return json.NewEncoder(os.Stdout).Encode(image)
		case "import":
			image, err := manager.Import(*path, *version)
			if err != nil {
				return err
			}
			return json.NewEncoder(os.Stdout).Encode(image)
		default:
			return fmt.Errorf("unknown image action")
		}
	case "http-probe":
		f := flag.NewFlagSet("http-probe", flag.ContinueOnError)
		target := f.String("target", "http://198.18.0.1:8080/", "HTTP probe destination")
		if err := f.Parse(args); err != nil {
			return err
		}
		if !strings.HasPrefix(*target, "http://") {
			return fmt.Errorf("probe requires an HTTP destination")
		}
		client := http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: nil}}
		response, err := client.Get(*target)
		if err != nil {
			return err
		}
		defer response.Body.Close()
		fmt.Printf("HTTP %s\n", response.Status)
		_, err = io.Copy(os.Stdout, io.LimitReader(response.Body, 32768))
		return err
	case "help", "--help", "-h":
		fmt.Println("Fiberlab: real-packet FTTH lab\n\nCommands:\n  serve    Start the local web application\n  doctor   Check runtime dependencies\n  netd     Run the privileged network helper\n  images   Download/import/list pinned official CHR images\n\nUse COMMAND --help for options.")
		return nil
	default:
		return fmt.Errorf("unknown command %q (use ftthlab help)", command)
	}
}
func defaultOwner() int {
	if value := os.Getenv("SUDO_UID"); value != "" {
		if n, err := strconv.Atoi(value); err == nil {
			return n
		}
	}
	return os.Getuid()
}
