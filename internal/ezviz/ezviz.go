package ezviz

import (
	"github.com/AlexxIT/go2rtc/internal/streams"
	"github.com/AlexxIT/go2rtc/pkg/core"
	"github.com/AlexxIT/go2rtc/pkg/ezviz"
)

func Init() {
	handler := func(source string) (core.Producer, error) {
		return ezviz.NewProducer(source)
	}

	streams.HandleFunc("ezviz", handler)
	streams.HandleFunc("hikconnect", handler)
}
