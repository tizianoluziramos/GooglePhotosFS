package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/signal"
	"path"
	"strings"
)

const version = "0.2.0"

func help() {
	fmt.Print(`GooglePhotosFS ` + version + `
  config [manual|auto|existing|path]   Configurar cuenta u obtener la ruta TOML
  mount [P:] | unmount [P:]            Montar o desmontar con WinFsp en Windows
  sync | about                        Actualizar índice o consultar cuota
  ls [directorio] | stat archivo      Listar o consultar un elemento
  metadata archivo | hash archivo    Leer metadatos o SHA-1
  set-description archivo texto      Cambiar descripción ("" para borrar)
  get remoto local | put local remoto Transferir un medio
  rm archivo                          Quitar ruta o enviar el medio a la papelera
`)
}

func printJSON(value any) error {
	out := json.NewEncoder(os.Stdout)
	out.SetIndent("", "  ")
	return out.Encode(value)
}

func transferOut(ctx context.Context, s *Store, node Node, target string) error {
	if node.Dir {
		return errors.New("la ruta es un directorio")
	}
	if node.Media.Key == "" {
		return errors.New("el medio no tiene clave de descarga")
	}
	var body io.Reader
	var err error
	if s.config.Photos.DownloadCache {
		download, err := s.downloads.open(*node.Media)
		if err != nil {
			return err
		}
		defer download.Close()
		stop := context.AfterFunc(ctx, s.downloads.cancel)
		defer stop()
		body = io.NewSectionReader(download, 0, math.MaxInt64)
	} else {
		stream, err := s.photos.download(ctx, node.Media.Key)
		if err != nil {
			return err
		}
		defer stream.Close()
		body = stream
	}
	if target == "-" {
		_, err = io.Copy(os.Stdout, body)
		return err
	}
	f, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		f.Close()
		if !ok {
			os.Remove(target)
		}
	}()
	if _, err = io.Copy(f, body); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	ok = true
	return nil
}

func validArgumentCount(args []string, min, max int) error {
	if len(args) < min || len(args) > max {
		return fmt.Errorf("argumentos inválidos para %s. Usá --help", args[0])
	}
	return nil
}
func run(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" || args[0] == "help" {
		help()
		return nil
	}
	if args[0] == "--version" || args[0] == "version" {
		fmt.Println(version)
		return nil
	}
	if len(args) == 2 && args[0] == "config" && args[1] == "path" {
		filename, err := configPath()
		if err == nil {
			fmt.Println(filename)
		}
		return err
	}
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	cmd := args[0]
	switch cmd {
	case "config":
		return configure(ctx, cfg, args[1:])
	case "mount", "unmount", "__mount":
		return platformMount(ctx, cfg.runtime(), args)
	}
	counts := map[string][2]int{
		"sync": {1, 1}, "about": {1, 1}, "ls": {1, 2}, "stat": {2, 2}, "metadata": {2, 2}, "hash": {2, 2},
		"set-description": {3, 3}, "get": {3, 3}, "put": {3, 3}, "rm": {2, 2},
	}
	count, ok := counts[cmd]
	if !ok {
		return fmt.Errorf("comando desconocido: %q", cmd)
	}
	if err = validArgumentCount(args, count[0], count[1]); err != nil {
		return err
	}
	s, err := openStore(cfg.runtime(), nativeNames())
	if err != nil {
		return err
	}
	defer s.close()
	if cmd == "about" {
		u, err := s.photos.usage(ctx)
		if err != nil {
			return err
		}
		return printJSON(u)
	}
	if err = s.sync(ctx, cmd == "sync"); err != nil {
		if s.catalog.Load() == nil || cmd == "sync" {
			return err
		}
		fmt.Fprintln(os.Stderr, err)
	}
	if cmd == "sync" {
		return nil
	}
	remote := ""
	if len(args) > 1 {
		remote = cleanVirtual(strings.ReplaceAll(args[1], "\\", "/"))
	}
	if cmd == "put" {
		remote = cleanVirtual(strings.ReplaceAll(args[2], "\\", "/"))
		if remote == "" {
			return errors.New("indicá un nombre de archivo remoto")
		}
		f, err := os.Open(args[1])
		if err != nil {
			return err
		}
		defer f.Close()
		dir := path.Join(s.config.Filesystem.Root, folder(remote))
		if dir == "." {
			dir = ""
		}
		if node, err := s.node(folder(remote)); err == nil && node.Dir {
			dir = node.Virt
		}
		item, err := s.put(ctx, f, path.Base(remote), dir)
		if err != nil {
			return err
		}
		return printJSON(map[string]any{"id": item.Key, "sha1": item.SHA1, "size": item.Size})
	}
	node, err := s.node(remote)
	if err != nil {
		return fmt.Errorf("%s: %w", remote, err)
	}
	if cmd == "ls" {
		if !node.Dir {
			return errors.New("la ruta no es un directorio")
		}
		for _, child := range s.catalog.Load().children[fold(node.Path)] {
			suffix := ""
			if child.Dir {
				suffix = "/"
			}
			fmt.Println(path.Base(child.Path) + suffix)
		}
		return nil
	}
	if cmd == "stat" {
		result := map[string]any{"path": node.Path, "directory": node.Dir}
		if node.Media != nil {
			result["id"], result["size"], result["sha1"] = node.Media.Key, node.Media.Size, node.Media.SHA1
			result["metadata"] = node.Media.metadata()
		}
		return printJSON(result)
	}
	if node.Dir {
		return errVirtualDirectory
	}
	switch cmd {
	case "metadata":
		return printJSON(node.Media.metadata())
	case "hash":
		fmt.Println(node.Media.SHA1)
		return nil
	case "set-description":
		return s.mutate(ctx, node, &args[2])
	case "rm":
		return s.mutate(ctx, node, nil)
	case "get":
		return transferOut(ctx, s, node, args[2])
	}
	return nil
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "GooglePhotosFS:", err)
		os.Exit(1)
	}
}
