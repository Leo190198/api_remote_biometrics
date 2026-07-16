//go:build windows && 386

package main

import (
	"os"
	"os/exec"
	"syscall"
	"time"
	"unsafe"
)

var procCreateMutexW = kernel32.NewProc("CreateMutexW")

const erroJaExiste syscall.Errno = 183

func instanciaUnica() bool {
	nome, err := syscall.UTF16PtrFromString(`Local\AgenteBiometriaGo`)
	if err != nil {
		return false
	}
	h, _, errno := procCreateMutexW.Call(0, 0, uintptr(unsafe.Pointer(nome)))
	if h == 0 {
		return false
	}
	return errno != erroJaExiste
}

func supervisor() {
	exe, err := os.Executable()
	if err != nil {
		os.Exit(1)
	}
	espera := 2 * time.Second
	for {
		cmd := exec.Command(exe)
		cmd.Env = append(os.Environ(), "BIO_FILHO=1")
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		inicio := time.Now()
		if err := cmd.Start(); err != nil {
			os.Exit(1)
		}
		if cmd.Wait() == nil {
			os.Exit(0)
		}
		time.Sleep(espera)
		if time.Since(inicio) > time.Minute {
			espera = 2 * time.Second
		} else if espera < time.Minute {
			espera *= 2
			if espera > time.Minute {
				espera = time.Minute
			}
		}
	}
}
