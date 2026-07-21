package model

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

var symbolRE = regexp.MustCompile(`^[A-Z][A-Z0-9.\-]{0,14}$`)

func Symbol(s string) string {
	s = strings.ToUpper(strings.TrimSpace(s))
	if !symbolRE.MatchString(s) {
		return ""
	}
	return s
}

type Item struct {
	ID, Symbol, Note string
	AddedAt          time.Time
}
type List struct {
	ID, Name, Color string
	Items           []Item
}
type Layout struct {
	Lists []List `json:"lists"`
}

type Store struct {
	mu   sync.RWMutex
	path string
	data Layout
}

func Open(path string) (*Store, error) {
	s := &Store{path: path, data: Layout{Lists: []List{{ID: "api", Name: "API", Color: "#9bdb4d"}, {ID: "focus", Name: "FOCUS", Color: "#ffd166"}, {ID: "watch", Name: "WATCH", Color: "#4cc9f0"}}}}
	b, err := os.ReadFile(path)
	if err == nil {
		if err = json.Unmarshal(b, &s.data); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	ensureAPI(&s.data)
	return s, nil
}
func (s *Store) Get() Layout {
	s.mu.RLock()
	defer s.mu.RUnlock()
	b, _ := json.Marshal(s.data)
	var out Layout
	_ = json.Unmarshal(b, &out)
	for i := range out.Lists {
		if out.Lists[i].Items == nil {
			out.Lists[i].Items = []Item{}
		}
	}
	return out
}
func (s *Store) Replace(v Layout) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ensureAPI(&v)
	if err := validate(&v); err != nil {
		return err
	}
	s.data = v
	return s.save()
}
func (s *Store) Add(symbol, listID, note string) (Item, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	symbol = Symbol(symbol)
	if symbol == "" {
		return Item{}, errors.New("invalid symbol")
	}
	if listID == "" {
		listID = "api"
	}
	target := -1
	for li := range s.data.Lists {
		if s.data.Lists[li].ID == listID {
			target = li
		}
	}
	if target < 0 {
		return Item{}, errors.New("watchlist not found")
	}
	// A repeat scanner alert is a priority signal, not a duplicate: promote the
	// existing row and move it if the request chose another watchlist.
	for li := range s.data.Lists {
		for i, it := range s.data.Lists[li].Items {
			if it.Symbol != symbol {
				continue
			}
			s.data.Lists[li].Items = append(s.data.Lists[li].Items[:i], s.data.Lists[li].Items[i+1:]...)
			if strings.TrimSpace(note) != "" {
				it.Note = strings.TrimSpace(note)
			}
			s.data.Lists[target].Items = append([]Item{it}, s.data.Lists[target].Items...)
			return it, s.save()
		}
	}
	it := Item{ID: strings.ToLower(symbol) + "-" + time.Now().Format("150405.000"), Symbol: symbol, Note: strings.TrimSpace(note), AddedAt: time.Now()}
	s.data.Lists[target].Items = append([]Item{it}, s.data.Lists[target].Items...)
	return it, s.save()
}
func ensureAPI(v *Layout) {
	api := -1
	for i := range v.Lists {
		if v.Lists[i].ID == "api" || strings.EqualFold(strings.TrimSpace(v.Lists[i].Name), "api") {
			api = i
			break
		}
	}
	if api < 0 {
		v.Lists = append([]List{{ID: "api", Name: "API", Color: "#9bdb4d", Items: []Item{}}}, v.Lists...)
		return
	}
	v.Lists[api].ID = "api"
	v.Lists[api].Name = "API"
	if v.Lists[api].Color == "" {
		v.Lists[api].Color = "#9bdb4d"
	}
	if api > 0 {
		list := v.Lists[api]
		v.Lists = append(v.Lists[:api], v.Lists[api+1:]...)
		v.Lists = append([]List{list}, v.Lists...)
	}
}
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for li := range s.data.Lists {
		for i, it := range s.data.Lists[li].Items {
			if it.ID == id {
				s.data.Lists[li].Items = append(s.data.Lists[li].Items[:i], s.data.Lists[li].Items[i+1:]...)
				return s.save()
			}
		}
	}
	return errors.New("ticker not found")
}
func validate(v *Layout) error {
	seen := map[string]bool{}
	if len(v.Lists) > 20 {
		return errors.New("too many watchlists")
	}
	for i := range v.Lists {
		v.Lists[i].Name = strings.TrimSpace(v.Lists[i].Name)
		if v.Lists[i].ID == "" || v.Lists[i].Name == "" || seen[v.Lists[i].ID] {
			return errors.New("invalid watchlist")
		}
		seen[v.Lists[i].ID] = true
		for j := range v.Lists[i].Items {
			v.Lists[i].Items[j].Symbol = Symbol(v.Lists[i].Items[j].Symbol)
			if v.Lists[i].Items[j].ID == "" || v.Lists[i].Items[j].Symbol == "" {
				return errors.New("invalid ticker")
			}
		}
	}
	return nil
}
func (s *Store) save() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(s.data, "", "  ")
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
