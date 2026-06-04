package chainworker

import "strings"

type evmModule struct {
	NativeCrypto string
}

func evmModuleFor(module string) (evmModule, bool) {
	switch strings.ToUpper(module) {
	case "BNB":
		return evmModule{NativeCrypto: "BNB"}, true
	case "ETH":
		return evmModule{NativeCrypto: "ETH"}, true
	case "MATIC":
		return evmModule{NativeCrypto: "MATIC"}, true
	case "AVAX":
		return evmModule{NativeCrypto: "AVAX"}, true
	case "ARBETH":
		return evmModule{NativeCrypto: "ARBETH"}, true
	case "OPETH":
		return evmModule{NativeCrypto: "OPETH"}, true
	default:
		return evmModule{}, false
	}
}

func (s *Server) isEVMModule() bool {
	_, ok := evmModuleFor(s.cfg.Module)
	return ok
}

func isBitcoinLikeModule(module string) bool {
	switch strings.ToUpper(module) {
	case "BTC", "LTC", "DOGE", "FIRO":
		return true
	default:
		return false
	}
}

func (s *Server) isBitcoinLikeModule() bool {
	return isBitcoinLikeModule(s.cfg.Module)
}

func (s *Server) nativeCrypto() string {
	if strings.EqualFold(s.cfg.Module, "TRON") {
		return "TRX"
	}
	if module, ok := evmModuleFor(s.cfg.Module); ok {
		return module.NativeCrypto
	}
	return s.cfg.Module
}
