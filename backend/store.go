package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type HistoryItem struct {
	URL   string `json:"url"`
	Title string `json:"title"`
	TS    int64  `json:"ts"`
}

type FavItem struct {
	Name string `json:"name"`
	URL  string `json:"url"`
	TS   int64  `json:"ts"`
}

// AppItem 是「网页应用」条目（对应极空间那种带图标和代理开关的网页入口）。
// Proxy=true 时页面走 /api/p/ 转发，false 时浏览器直连目标地址。
type AppItem struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	URL   string `json:"url"`
	Note  string `json:"note"`
	Color string `json:"color"`
	Proxy   bool   `json:"proxy"`
	Desktop bool   `json:"desktop"`
	// UA 显式记下打开这个应用时用的是哪套 UA（mobile / desktop）；
	// Desktop 保留给老数据与老接口，前端优先读 UA。
	UA   string  `json:"ua,omitempty"`
	// Zoom 是这个应用默认的缩放比例（1 = 100%，0 表示没设过）。
	Zoom float64 `json:"zoom,omitempty"`
	TS   int64   `json:"ts"`
}

type Store struct {
	mu  sync.Mutex
	dir string
}

func NewStore(dir string) *Store { return &Store{dir: dir} }

func (s *Store) readList(name string, v any) {
	b, err := os.ReadFile(filepath.Join(s.dir, name))
	if err != nil || len(b) == 0 {
		return
	}
	_ = json.Unmarshal(b, v)
}

func (s *Store) writeList(name string, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	p := filepath.Join(s.dir, name)
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return
	}
	_ = os.Rename(tmp, p)
}

func (s *Store) HistoryHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		switch r.Method {
		case http.MethodGet:
			s.mu.Lock()
			var list []HistoryItem
			s.readList("history.json", &list)
			s.mu.Unlock()
			if list == nil {
				list = []HistoryItem{}
			}
			json.NewEncoder(w).Encode(list)

		case http.MethodPost:
			var in HistoryItem
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				http.Error(w, `{"error":"bad body"}`, http.StatusBadRequest)
				return
			}
			in.URL = strings.TrimSpace(in.URL)
			if in.URL == "" {
				http.Error(w, `{"error":"url required"}`, http.StatusBadRequest)
				return
			}
			in.TS = time.Now().Unix()

			s.mu.Lock()
			var list []HistoryItem
			s.readList("history.json", &list)
			kept := list[:0]
			for _, it := range list {
				if it.URL != in.URL {
					kept = append(kept, it)
				}
			}
			list = append([]HistoryItem{in}, kept...)
			if len(list) > 200 {
				list = list[:200]
			}
			s.writeList("history.json", list)
			s.mu.Unlock()
			w.Write([]byte(`{"ok":true}`))

		case http.MethodDelete:
			// 带 ?url= 只删这一条，不带则清空全部
			target := strings.TrimSpace(r.URL.Query().Get("url"))
			s.mu.Lock()
			if target == "" {
				s.writeList("history.json", []HistoryItem{})
			} else {
				var list []HistoryItem
				s.readList("history.json", &list)
				kept := make([]HistoryItem, 0, len(list))
				for _, it := range list {
					if it.URL != target {
						kept = append(kept, it)
					}
				}
				s.writeList("history.json", kept)
			}
			s.mu.Unlock()
			w.Write([]byte(`{"ok":true}`))

		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}
}

func (s *Store) FavHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		switch r.Method {
		case http.MethodGet:
			s.mu.Lock()
			var list []FavItem
			s.readList("fav.json", &list)
			s.mu.Unlock()
			if list == nil {
				list = []FavItem{}
			}
			json.NewEncoder(w).Encode(list)

		case http.MethodPost:
			var in FavItem
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				http.Error(w, `{"error":"bad body"}`, http.StatusBadRequest)
				return
			}
			in.URL = strings.TrimSpace(in.URL)
			in.Name = strings.TrimSpace(in.Name)
			if in.URL == "" {
				http.Error(w, `{"error":"url required"}`, http.StatusBadRequest)
				return
			}
			if in.Name == "" {
				in.Name = in.URL
			}
			in.TS = time.Now().Unix()

			s.mu.Lock()
			var list []FavItem
			s.readList("fav.json", &list)
			kept := list[:0]
			for _, it := range list {
				if it.URL != in.URL {
					kept = append(kept, it)
				}
			}
			list = append([]FavItem{in}, kept...)
			s.writeList("fav.json", list)
			s.mu.Unlock()
			w.Write([]byte(`{"ok":true}`))

		case http.MethodDelete:
			u := r.URL.Query().Get("url")
			s.mu.Lock()
			var list []FavItem
			s.readList("fav.json", &list)
			kept := list[:0]
			for _, it := range list {
				if it.URL != u {
					kept = append(kept, it)
				}
			}
			s.writeList("fav.json", kept)
			s.mu.Unlock()
			w.Write([]byte(`{"ok":true}`))

		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}
}

// AppsHandler 维护「网页应用」列表：GET 列表、POST 新增/按 id 更新、DELETE?id= 删除。
func (s *Store) AppsHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		switch r.Method {
		case http.MethodGet:
			s.mu.Lock()
			var list []AppItem
			s.readList("apps.json", &list)
			s.mu.Unlock()
			if list == nil {
				list = []AppItem{}
			}
			json.NewEncoder(w).Encode(list)

		case http.MethodPost:
			var in AppItem
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				http.Error(w, `{"error":"bad body"}`, http.StatusBadRequest)
				return
			}
			in.Name = strings.TrimSpace(in.Name)
			in.URL = strings.TrimSpace(in.URL)
			in.Note = strings.TrimSpace(in.Note)
			if in.URL == "" {
				http.Error(w, `{"error":"url required"}`, http.StatusBadRequest)
				return
			}
			if in.Name == "" {
				in.Name = in.URL
			}
			if in.Color == "" {
				in.Color = "#5b8def"
			}
			if in.UA != "desktop" {
				in.UA = "mobile"
			}
			if in.Zoom < 0.3 || in.Zoom > 2.2 {
				in.Zoom = 0
			}
			in.TS = time.Now().Unix()

			s.mu.Lock()
			var list []AppItem
			s.readList("apps.json", &list)
			if in.ID == "" {
				in.ID = strconv.FormatInt(time.Now().UnixNano(), 36)
			}
			replaced := false
			for i := range list {
				if list[i].ID == in.ID {
					list[i] = in
					replaced = true
					break
				}
			}
			if !replaced {
				list = append([]AppItem{in}, list...)
			}
			if len(list) > 60 {
				list = list[:60]
			}
			s.writeList("apps.json", list)
			s.mu.Unlock()
			json.NewEncoder(w).Encode(in)

		case http.MethodDelete:
			id := r.URL.Query().Get("id")
			s.mu.Lock()
			var list []AppItem
			s.readList("apps.json", &list)
			kept := list[:0]
			for _, it := range list {
				if it.ID != id {
					kept = append(kept, it)
				}
			}
			s.writeList("apps.json", kept)
			s.mu.Unlock()
			w.Write([]byte(`{"ok":true}`))

		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}
}
