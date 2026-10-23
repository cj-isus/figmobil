// figmobil provides explicit encrypted exchange and local comparison.
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
	"path/filepath"
	"time"

	"figmobil/internal/channel"
	"figmobil/internal/compare"
	"figmobil/internal/secure"
	"figmobil/internal/server"
	"figmobil/internal/transferlimit"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "figmobil:", err)
		os.Exit(1)
	}
}

func run() error {
	release, err := secure.AcquireOwner()
	if err != nil {
		return fmt.Errorf("другое окно figmobil уже работает или владение устройством недоступно")
	}
	defer release()
	localData := os.Getenv("LOCALAPPDATA")
	if !filepath.IsAbs(localData) {
		return fmt.Errorf("не удалось определить локальную папку данных Windows")
	}
	manager, err := channel.New(filepath.Join(localData, "figmobil"), transferlimit.New())
	if err != nil {
		return fmt.Errorf("не удалось открыть защищённое состояние figmobil; проверьте профиль Windows и данные приложения")
	}
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
	handler.EnableChannel(manager)
	service := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 100 * time.Second, IdleTimeout: 30 * time.Second, ErrorLog: log.New(os.Stderr, "figmobil http: ", 0)}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	stopped := make(chan struct{})
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := service.Shutdown(shutdown); err != nil {
			_ = service.Close()
		}
		manager.Close()
		close(stopped)
	}()
	fmt.Println("figmobil 0.2.0-preview — ручной зашифрованный обмен через Figma")
	fmt.Println("Откройте в браузере: http://" + listener.Addr().String())
	fmt.Println("Экспорт и импорт запускаются кнопками; запись — после предпросмотра и подтверждения.")
	fmt.Println("Для остановки нажмите Ctrl+C или закройте это окно. Фоновая синхронизация отсутствует.")
	err = service.Serve(listener)
	stop()
	<-stopped
	if err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}
