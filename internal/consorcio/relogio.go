package consorcio

import (
	"os"
	"sync"
)

var (
	relogioMu sync.Mutex
	relogio   uint64
	MeuID     string
)

func init() {
	MeuID = os.Getenv("ID_EMPRESA")
	if MeuID == "" {
		MeuID = os.Getenv("URL_EMPRESA")
		if MeuID == "" {
			MeuID = "empresa_local"
		}
	}
}

// Tick incrementa o relógio de Lamport antes de enviar uma requisição
func Tick() uint64 {
	relogioMu.Lock()
	defer relogioMu.Unlock()
	relogio++
	return relogio
}

// Atualizar ajusta o relógio local de Lamport ao receber uma requisição
func Atualizar(recebido uint64) uint64 {
	relogioMu.Lock()
	defer relogioMu.Unlock()
	if recebido > relogio {
		relogio = recebido
	}
	relogio++
	return relogio
}

// ObterRelogio retorna o valor atual do relógio
func ObterRelogio() uint64 {
	relogioMu.Lock()
	defer relogioMu.Unlock()
	return relogio
}

