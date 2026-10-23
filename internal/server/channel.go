package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"time"

	"figmobil/internal/channel"
)

const MaxChannelRequestBytes = 2 << 20

func (h *Handler) handleChannel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeError(w, 405, "METHOD_NOT_ALLOWED", "Действие запускается только кнопкой.")
		return
	}
	if r.Header.Get("Origin") != h.origin || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Figmobil-Token")), []byte(h.token)) != 1 {
		writeError(w, 403, "SESSION_REJECTED", "Откройте адрес из текущего окна figmobil.")
		return
	}
	if h.channel == nil {
		writeError(w, 503, "CHANNEL_UNAVAILABLE", "Канал в этом сеансе недоступен.")
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeError(w, 415, "CONTENT_TYPE", "Ожидается JSON.")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, MaxChannelRequestBytes)
	defer r.Body.Close()
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var request channel.Request
	if err := decoder.Decode(&request); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, 413, "REQUEST_TOO_LARGE", "Слишком большой запрос.")
		} else {
			writeError(w, 400, "INVALID_REQUEST", "Некорректные параметры действия.")
		}
		return
	}
	if decoder.Decode(new(any)) != io.EOF {
		writeError(w, 400, "INVALID_REQUEST", "Ожидается один объект параметров.")
		return
	}
	select {
	case h.jobs <- struct{}{}:
		defer func() { <-h.jobs }()
	default:
		writeError(w, 409, "JOB_BUSY", "Другая операция ещё выполняется.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	written := false
	err = h.channel.Do(ctx, request, func(response channel.Response) error {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		written = true
		err := json.NewEncoder(w).Encode(response)
		if err == nil {
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
		}
		return err
	})
	if err == nil || written {
		return
	}
	var problem *channel.Error
	if errors.As(err, &problem) {
		status := 422
		if problem.Code == "RATE_LIMIT" {
			status = 429
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(struct {
			Error *channel.Error `json:"error"`
		}{problem})
		return
	}
	writeError(w, 500, "CHANNEL_ERROR", "Операция не завершена. Обновите состояние и проверьте восстановление.")
}
