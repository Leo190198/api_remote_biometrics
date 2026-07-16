//go:build windows && 386

package main

import (
	"io"
	"log"
	"os"
	"path/filepath"
)

var logger = log.New(io.Discard, "", log.Ldate|log.Ltime|log.Lmicroseconds)

func iniciaLog() {
	dir, err := garanteDiretorioDados()
	if err != nil {
		return
	}
	path := filepath.Join(dir, "agente.log")
	if info, err := os.Stat(path); err == nil && info.Size() > 5<<20 {
		_ = os.Remove(path + ".1")
		_ = os.Rename(path, path+".1")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err == nil {
		logger.SetOutput(f)
	}
}

func registraErro(formato string, args ...any) {
	logger.Printf("ERRO: "+formato, args...)
}
