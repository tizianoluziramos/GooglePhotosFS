package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf16"
)

type Node struct {
	Path, Virt string
	Dir        bool
	Media      *Media
}
type Catalog struct {
	nodes    map[string]Node
	children map[string][]Node
}

func cleanVirtual(s string) string { return strings.TrimPrefix(path.Clean("/"+s), "/") }
func folder(s string) string {
	if i := strings.LastIndexByte(s, '/'); i >= 0 {
		return s[:i]
	}
	return ""
}
func fold(s string) string { return strings.Map(unicode.ToUpper, s) }

func canonicalMode(mode string) string {
	switch strings.ToLower(mode) {
	case "calendar", "calender", "day", "week":
		return "calendar"
	case "month", "year_month":
		return "month"
	case "year":
		return "year"
	case "description", "description_path":
		return "description_path"
	default:
		return "flat"
	}
}

func taggedCaption(s string) (string, []string) {
	if at := strings.Index(s, "{paths:"); at >= 0 {
		after := s[at+len("{paths:"):]
		if strings.HasPrefix(after, "[") {
			decoder := json.NewDecoder(strings.NewReader(after))
			var paths []string
			if err := decoder.Decode(&paths); err == nil {
				end := int(decoder.InputOffset())
				if end < len(after) && after[end] == '}' {
					return strings.TrimSpace(s[:at] + after[end+1:]), paths
				}
			}
		}
	}
	for _, tag := range []struct{ start, end string }{
		{"{paths:[", "]}"}, {"[paths:", "]"}, {"[path:", "]"},
	} {
		at := strings.Index(s, tag.start)
		if at < 0 {
			continue
		}
		begin := at + len(tag.start)
		end := strings.Index(s[begin:], tag.end)
		if end < 0 {
			return strings.TrimSpace(s), nil
		}
		end += begin
		var paths []string
		for _, p := range strings.Split(s[begin:end], ",") {
			p = strings.TrimSpace(strings.Trim(strings.TrimSpace(p), "\""))
			if p != "" {
				paths = append(paths, p)
			}
		}
		return strings.TrimSpace(s[:at] + s[end+len(tag.end):]), paths
	}
	return strings.TrimSpace(s), nil
}

func withPaths(caption string, folders []string) string {
	raw, _ := json.Marshal(folders)
	if caption != "" {
		caption += " "
	}
	return caption + "{paths:" + string(raw) + "}"
}

func virtualFolders(mode string, item *Media) []string {
	t := time.UnixMilli(item.Taken).UTC()
	y, m, d := t.Format("2006"), t.Format("2006-01"), t.Format("2006-01-02")
	switch mode {
	case "year":
		return []string{y}
	case "month":
		return []string{y + "/" + m}
	case "calendar":
		return []string{y + "/" + m + "/" + d}
	case "description_path":
		_, tags := taggedCaption(item.Caption)
		if len(tags) != 0 {
			for i, tag := range tags {
				tags[i] = cleanVirtual(tag)
			}
			return tags
		}
	}
	return []string{""}
}

func nativeComponent(s string, enc nameEncoding) string {
	out := enc.encode(s)
	stem, _, _ := strings.Cut(out, ".")
	stem = fold(strings.TrimRight(stem, " "))
	switch stem {
	case "CON", "PRN", "AUX", "NUL", "CONIN$", "CONOUT$":
		out = "‛" + out
	default:
		if len([]rune(stem)) == 4 && (strings.HasPrefix(stem, "COM") || strings.HasPrefix(stem, "LPT")) &&
			strings.ContainsRune("123456789¹²³", []rune(stem)[3]) {
			out = "‛" + out
		}
	}
	if len(utf16.Encode([]rune(out))) > 255 {
		digest := sha256.Sum256([]byte(out))
		out = fitName(out, "_"+hex.EncodeToString(digest[:8]))
	}
	return out
}

func fitName(name, suffix string) string {
	ext := path.Ext(name)
	capacity := 255 - len(utf16.Encode([]rune(suffix+ext)))
	if capacity < 1 {
		ext = ""
		capacity = 255 - len(utf16.Encode([]rune(suffix)))
	}
	var prefix strings.Builder
	for _, r := range strings.TrimSuffix(name, ext) {
		size := 1
		if r > 0xffff {
			size = 2
		}
		if size > capacity {
			break
		}
		prefix.WriteRune(r)
		capacity -= size
	}
	return prefix.String() + suffix + ext
}

func project(items []Media, mode, root string, enc nameEncoding, native bool) *Catalog {
	result := &Catalog{nodes: map[string]Node{"": {Dir: true, Virt: root}}, children: make(map[string][]Node)}
	encode := enc.encode
	if native {
		encode = func(value string) string { return nativeComponent(value, enc) }
	}
	preferred := make(map[string]*Media)
	var order []string
	for i := range items {
		item := &items[i]
		id := item.Dedup
		if id == "" {
			id = item.Key
		}
		if prior, exists := preferred[id]; exists {
			if item.Canonical && !prior.Canonical {
				preferred[id] = item
			}
			continue
		}
		preferred[id] = item
		order = append(order, id)
	}
	dirs := map[string]string{root: ""}
	displayDir := func(virt string) string {
		absolute := root
		display := ""
		for _, component := range strings.Split(virt, "/") {
			if component == "" {
				continue
			}
			absolute = path.Join(absolute, component)
			if existing, ok := dirs[absolute]; ok {
				display = existing
				continue
			}
			name := encode(component)
			target := path.Join(display, name)
			for i := 1; ; i++ {
				if _, taken := result.nodes[fold(target)]; !taken {
					break
				}
				target = path.Join(display, fitName(name, fmt.Sprintf("_%d", i)))
			}
			result.nodes[fold(target)] = Node{Path: target, Virt: absolute, Dir: true}
			dirs[absolute], display = target, target
		}
		return display
	}
	type candidate struct {
		node Node
		id   string
	}
	var candidates []candidate
	for _, id := range order {
		item := preferred[id]
		if item.Name == "" {
			continue
		}
		for _, virtual := range virtualFolders(mode, item) {
			relative := virtual
			if root != "" {
				switch {
				case virtual == root:
					relative = ""
				case strings.HasPrefix(virtual, root+"/"):
					relative = virtual[len(root)+1:]
				default:
					continue
				}
			}
			dir := displayDir(relative)
			name := encode(standardEncoding.decode(item.Name))
			if name == "" {
				continue
			}
			candidates = append(candidates, candidate{Node{Path: path.Join(dir, name), Virt: virtual, Media: item}, id})
		}
	}
	reserved := make(map[string]bool)
	for _, entry := range candidates {
		reserved[fold(entry.node.Path)] = true
	}
	for _, entry := range candidates {
		node := entry.node
		key := fold(node.Path)
		if displayed, exists := result.nodes[key]; exists {
			if displayed.Media != nil && displayed.Media.Key == node.Media.Key {
				continue
			}
			dir, name := folder(node.Path), path.Base(node.Path)
			suffix := entry.id
			if len(suffix) > 70 || strings.ContainsAny(suffix, "/\\:\"?*<>|") {
				digest := sha256.Sum256([]byte(suffix))
				suffix = hex.EncodeToString(digest[:12])
			}
			node.Path = path.Join(dir, fitName(name, "_"+suffix))
			for i := 1; ; i++ {
				_, exists := result.nodes[fold(node.Path)]
				if !exists && !reserved[fold(node.Path)] {
					break
				}
				node.Path = path.Join(dir, fitName(name, fmt.Sprintf("_%s_%d", suffix, i)))
			}
		}
		result.nodes[fold(node.Path)] = node
	}
	for _, node := range result.nodes {
		if node.Path != "" {
			parent := fold(folder(node.Path))
			result.children[parent] = append(result.children[parent], node)
		}
	}
	for _, nodes := range result.children {
		sort.Slice(nodes, func(i, j int) bool { return fold(nodes[i].Path) < fold(nodes[j].Path) })
	}
	return result
}
