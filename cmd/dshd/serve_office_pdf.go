package main

// PARITY-W4-OFFICE：officeToPdf.generation/render 的宿主实现。
// 真实渲染器在 internal/officepdf（Word COM / LibreOffice）；本层只做注入与错误映射。

import (
	"context"
	"errors"

	"dshgo/internal/officepdf"
	"dshgo/internal/transport"
)

type officePDFAPI struct {
	provider *officepdf.Provider
}

func newOfficePDFAPI(home string) *officePDFAPI {
	return &officePDFAPI{provider: officepdf.Open(home)}
}

func (a *officePDFAPI) Generation(context.Context) (string, error) {
	return a.provider.GenerationID(), nil
}

func (a *officePDFAPI) Render(ctx context.Context, scope, path, priority string) (transport.OfficialOfficePDFResult, error) {
	doc, err := a.provider.Render(ctx, scope, path, priority)
	if err != nil {
		var oe *officepdf.Error
		if errors.As(err, &oe) {
			status := 502
			switch oe.Code {
			case "renderer_unavailable":
				status = 503
			case "bad_request", "unsupported":
				status = 400
			case "not_found":
				status = 404
			case "render_timeout":
				status = 504
			}
			return transport.OfficialOfficePDFResult{}, &transport.OfficePDFError{Status: status, Code: oe.Code, Message: oe.Message}
		}
		return transport.OfficialOfficePDFResult{}, err
	}
	return transport.OfficialOfficePDFResult{MissingFonts: doc.MissingFonts, Generation: doc.Generation,
		Offset: doc.Offset, Data: doc.Data, Eof: doc.Eof, AbsolutePath: doc.AbsolutePath,
		Version: doc.Version, Bytes: doc.Bytes}, nil
}

var _ transport.OfficialOfficePDFAPI = (*officePDFAPI)(nil)
