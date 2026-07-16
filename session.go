//go:build windows && 386

package main

import (
	"encoding/binary"
	"os"
	"syscall"
	"unsafe"
)

var (
	iphlpapi         = syscall.NewLazyDLL("iphlpapi.dll")
	kernel32         = syscall.NewLazyDLL("kernel32.dll")
	procGetTCPTable  = iphlpapi.NewProc("GetExtendedTcpTable")
	procPidToSession = kernel32.NewProc("ProcessIdToSessionId")
)

const (
	afInet                  = 2
	tcpTableOwnerPIDAll     = 5
	localhostLE             = 0x0100007F
	errorInsufficientBuffer = 122
	tamanhoLinhaTCP         = 24
)

func portaHost(valor uint32) uint16 {
	return uint16((valor&0xFF)<<8 | (valor>>8)&0xFF)
}

func pidNaTabelaTCP(buf []byte, portaOrigem, portaDestino uint16) (uint32, bool) {
	if len(buf) < 4 {
		return 0, false
	}
	quantidade := int(binary.LittleEndian.Uint32(buf[:4]))
	if quantidade > (len(buf)-4)/tamanhoLinhaTCP {
		return 0, false
	}
	for i := 0; i < quantidade; i++ {
		linha := buf[4+i*tamanhoLinhaTCP : 4+(i+1)*tamanhoLinhaTCP]
		localAddr := binary.LittleEndian.Uint32(linha[4:8])
		localPort := binary.LittleEndian.Uint32(linha[8:12])
		remoteAddr := binary.LittleEndian.Uint32(linha[12:16])
		remotePort := binary.LittleEndian.Uint32(linha[16:20])
		pid := binary.LittleEndian.Uint32(linha[20:24])
		if localAddr == localhostLE && remoteAddr == localhostLE &&
			portaHost(localPort) == portaOrigem && portaHost(remotePort) == portaDestino {
			return pid, true
		}
	}
	return 0, false
}

func pidDaConexao(portaOrigem, portaDestino uint16) (uint32, bool) {
	var tamanho uint32
	for tentativa := 0; tentativa < 4; tentativa++ {
		r, _, _ := procGetTCPTable.Call(0, uintptr(unsafe.Pointer(&tamanho)), 0, afInet, tcpTableOwnerPIDAll, 0)
		if r != errorInsufficientBuffer || tamanho < 4 {
			return 0, false
		}
		buf := make([]byte, tamanho)
		r, _, _ = procGetTCPTable.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&tamanho)),
			0, afInet, tcpTableOwnerPIDAll, 0)
		if r == errorInsufficientBuffer {
			continue
		}
		if r != 0 {
			return 0, false
		}
		return pidNaTabelaTCP(buf[:tamanho], portaOrigem, portaDestino)
	}
	return 0, false
}

func sessaoDoPID(pid uint32) (uint32, bool) {
	var sid uint32
	r, _, _ := procPidToSession.Call(uintptr(pid), uintptr(unsafe.Pointer(&sid)))
	return sid, r != 0
}

func mesmaSessao(portaOrigem, portaDestino uint16) (bool, bool) {
	pid, ok := pidDaConexao(portaOrigem, portaDestino)
	if !ok {
		return false, false
	}
	sessaoCliente, okCliente := sessaoDoPID(pid)
	sessaoAgente, okAgente := sessaoDoPID(uint32(os.Getpid()))
	if !okCliente || !okAgente {
		return false, false
	}
	return sessaoCliente == sessaoAgente, true
}
