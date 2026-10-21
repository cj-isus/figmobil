// Package server exposes the read-only comparison component on authenticated loopback.
package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"figmobil/internal/compare"
	"figmobil/internal/webui"
)

const MaxRequestBytes = 32 * 1024

type errorBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type compareFunc func(context.Context, compare.Request, compare.Limits) (compare.Report, error)

// Handler does not expose arbitrary files or invoke Git, a shell, or imported code.
type Handler struct {
	authority string
	origin    string
	token     string
	limits    compare.Limits
	jobs      chan struct{}
	run       compareFunc
}

// New requires an explicit IPv4 loopback authority and an unguessable per-run token.
func New(authority, token string, limits compare.Limits) (*Handler, error) {
	host, portText, err := net.SplitHostPort(authority)
	port, portErr := strconv.Atoi(portText)
	if err != nil || host != "127.0.0.1" || portErr != nil || port < 1 || port > 65535 {
		return nil, errors.New("server must bind to an explicit 127.0.0.1 port")
	}
	if len(token) < 32 || strings.ContainsAny(token, "\"<>& \r\n") {
		return nil, errors.New("invalid session token")
	}
	return &Handler{authority: authority, origin: "http://" + authority, token: token, limits: limits, jobs: make(chan struct{}, 1), run: compare.Run}, nil
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:; base-uri 'none'; frame-ancestors 'none'; form-action 'none'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
	defer func() {
		if recover() != nil {
			writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Внутренняя ошибка. Файлы не изменялись.")
		}
	}()
	if r.Host != h.authority || (r.Header.Get("Origin") != "" && r.Header.Get("Origin") != h.origin) {
		writeError(w, http.StatusForbidden, "ORIGIN_REJECTED", "Этот запрос не относится к локальному окну figmobil.")
		return
	}
	if r.URL.Path == "/api/compare" {
		h.handleCompare(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Операция недоступна.")
		return
	}
	assets := map[string]string{"/": "index.html", "/app.js": "app.js", "/styles.css": "styles.css", "/view-model.mjs": "view-model.mjs"}
	asset, exists := assets[r.URL.Path]
	if !exists {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Страница не найдена.")
		return
	}
	content, err := webui.Files.ReadFile(asset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "ASSET_UNAVAILABLE", "Не удалось открыть интерфейс.")
		return
	}
	if asset == "index.html" {
		content = []byte(strings.ReplaceAll(string(content), "__FIGMOBIL_TOKEN__", h.token))
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
	} else if strings.HasSuffix(asset, ".css") {
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
	} else {
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(content)))
	if r.Method == http.MethodGet {
		_, _ = w.Write(content)
	}
}

func (h *Handler) handleCompare(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Сравнение запускается только явным запросом.")
		return
	}
	if r.Header.Get("Origin") != h.origin || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Figmobil-Token")), []byte(h.token)) != 1 {
		writeError(w, http.StatusForbidden, "SESSION_REJECTED", "Откройте адрес из текущего окна figmobil и повторите действие.")
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "CONTENT_TYPE", "Ожидается запрос JSON.")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, MaxRequestBytes)
	defer r.Body.Close()
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var request compare.Request
	if err := decoder.Decode(&request); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "REQUEST_TOO_LARGE", "Слишком длинные параметры запроса.")
		} else {
			writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "Не удалось прочитать параметры сравнения.")
		}
		return
	}
	if decoder.Decode(new(any)) != io.EOF {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "Ожидается один объект параметров.")
		return
	}
	select {
	case h.jobs <- struct{}{}:
		defer func() { <-h.jobs }()
	default:
		writeError(w, http.StatusConflict, "JOB_BUSY", "Сравнение уже выполняется. Дождитесь завершения или отмените его.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	result, err := h.run(ctx, request, h.limits)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			writeError(w, http.StatusRequestTimeout, "CANCELLED", "Сравнение отменено или превысило время ожидания.")
		} else {
			// The domain error is rendered only in the user's local UI, never logged.
			writeError(w, http.StatusUnprocessableEntity, compare.ErrorCode(err), fmt.Sprint(err))
		}
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(result)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	var body errorBody
	body.Error.Code, body.Error.Message = code, message
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
