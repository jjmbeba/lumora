// Command dev starts the local backend APIs and workers as a supervised group.
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type service struct {
	name string
	args []string
}

var services = []service{
	{name: "booking-api", args: []string{"run", "./cmd/booking-api"}},
	{name: "payment-api", args: []string{"run", "./cmd/payment-api"}},
	{name: "payment-worker", args: []string{"run", "./cmd/payment-worker"}},
	{name: "ticket-email-worker", args: []string{"run", "./cmd/ticket-email-worker"}},
	{name: "staff-invitation-worker", args: []string{"run", "./cmd/staff-invitation-worker"}},
}

func main() {
	if err := run(); err != nil {
		log.Printf("backend dev launcher: %v", err)
		os.Exit(1)
	}
}

func run() error {
	backendDir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get working directory: %w", err)
	}
	if filepath.Base(backendDir) != "backend" {
		return errors.New("run this command from the backend directory")
	}

	if err := loadDotEnv(filepath.Join(filepath.Dir(backendDir), ".env")); err != nil {
		return err
	}
	if appEnv := os.Getenv("APP_ENV"); appEnv != "" && appEnv != "development" {
		return errors.New("cmd/dev only runs with APP_ENV=development")
	}
	if err := os.Setenv("APP_ENV", "development"); err != nil {
		return fmt.Errorf("set development environment: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cmds := make([]*exec.Cmd, 0, len(services))
	for _, svc := range services {
		cmd := exec.Command("go", svc.args...)
		cmd.Dir = backendDir
		cmd.Env = os.Environ()
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmds = append(cmds, cmd)
	}

	exits := make(chan error, len(cmds))
	started := 0
	for i, cmd := range cmds {
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			return fmt.Errorf("open %s stdout: %w", services[i].name, err)
		}
		stderr, err := cmd.StderrPipe()
		if err != nil {
			return fmt.Errorf("open %s stderr: %w", services[i].name, err)
		}
		if err := cmd.Start(); err != nil {
			stopGroups(cmds[:i], exits, started)
			return fmt.Errorf("start %s: %w", services[i].name, err)
		}
		started++
		go func(name string, cmd *exec.Cmd) {
			err := cmd.Wait()
			if err != nil {
				err = fmt.Errorf("%s exited: %w", name, err)
			}
			exits <- err
		}(services[i].name, cmd)
		go prefixOutput(services[i].name, stdout)
		go prefixOutput(services[i].name, stderr)
		log.Printf("started %s", services[i].name)
	}

	select {
	case <-ctx.Done():
		log.Print("stopping backend services")
		stopGroups(cmds, exits, len(cmds))
		return nil
	case err := <-exits:
		stop()
		stopGroups(cmds, exits, len(cmds)-1)
		return err
	}
}

func stopGroups(cmds []*exec.Cmd, exits <-chan error, remaining int) {
	for _, cmd := range cmds {
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		}
	}
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for remaining > 0 {
		select {
		case <-exits:
			remaining--
		case <-timer.C:
			remaining = 0
			for _, cmd := range cmds {
				if cmd.Process != nil {
					_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
				}
			}
		}
	}
}

func prefixOutput(name string, input io.Reader) {
	scanner := bufio.NewScanner(input)
	for scanner.Scan() {
		log.Printf("[%s] %s", name, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		log.Printf("[%s] read output: %v", name, err)
	}
}

func loadDotEnv(path string) error {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open .env: %w", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for lineNo := 1; scanner.Scan(); lineNo++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, value, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(key) == "" {
			return fmt.Errorf(".env line %d must be KEY=VALUE", lineNo)
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if strings.HasPrefix(value, "\"") || strings.HasPrefix(value, "'") {
			if strings.HasPrefix(value, "\"") {
				unquoted, err := strconv.Unquote(value)
				if err != nil {
					return fmt.Errorf(".env line %d: %w", lineNo, err)
				}
				value = unquoted
			} else if strings.HasSuffix(value, "'") && len(value) >= 2 {
				value = value[1 : len(value)-1]
			}
		}
		if _, exists := os.LookupEnv(key); !exists {
			if err := os.Setenv(key, value); err != nil {
				return fmt.Errorf("set .env variable %s: %w", key, err)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read .env: %w", err)
	}
	return nil
}
