// figmobil provides an explicit, read-only local comparison UI.
package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"time"

	"figmobil/internal/compare"
	"figmobil/internal/server"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "figmobil:", err)
		os.Exit(1)
	}
}

func run() error {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("не удалось открыть локальный интерфейс: %w", err)
	}
	defer listener.Close()
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return fmt.Errorf("не удалось создать сеанс: %w", err)
	}
	handler, err := server.New(listener.Addr().String(), base64.RawURLEncoding.EncodeToString(secret), compare.DefaultLimits())
	if err != nil {
		return err
	}
	service := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 100 * time.Second, IdleTimeout: 30 * time.Second, ErrorLog: log.New(os.Stderr, "figmobil http: ", 0)}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = service.Shutdown(shutdown)
	}()
	fmt.Println("figmobil 0.1.0-preview — локальное сравнение проектов")
	fmt.Println("Откройте в браузере: http://" + listener.Addr().String())
	fmt.Println("Файлы только читаются. Для остановки нажмите Ctrl+C или закройте это окно.")
	if err := service.Serve(listener); err != nil && err != http.ErrServerClosed {
		stop()
		return err
	}
	stop()
	return nil
}
