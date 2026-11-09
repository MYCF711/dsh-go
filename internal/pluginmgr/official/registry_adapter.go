package official

import (
	"context"
	"fmt"
	"strings"
	"time"

	"dshgo/internal/pluginmgr"
	"dshgo/internal/transport"
)

// materializeRegistrySpec 把 registry/tarball 规格落成可交给 Manager 的本地
// 目录。下载/解包是真实 HTTP；失败原样返回（调用方折成结构化 refused）。
func (a *adapter) materializeRegistrySpec(spec string, registry *string) (string, pluginmgr.ResolvedPackage, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	kind := pluginmgr.SourceKind(spec)
	switch kind {
	case "registry":
		client := pluginmgr.RegistryFromEnv(registry)
		if client == nil {
			return "", pluginmgr.ResolvedPackage{}, fmt.Errorf(
				"没有配置 registry：设置 DSHD_PLUGIN_REGISTRY 或 options.registry")
		}
		resolved, err := client.Resolve(ctx, spec)
		if err != nil {
			return "", pluginmgr.ResolvedPackage{}, err
		}
		local, err := client.Download(ctx, resolved, pluginmgr.CacheDir())
		if err != nil {
			return "", pluginmgr.ResolvedPackage{}, err
		}
		return local, resolved, nil
	case "tarball":
		local, err := pluginmgr.DownloadTarball(ctx, spec, pluginmgr.CacheDir())
		if err != nil {
			return "", pluginmgr.ResolvedPackage{}, err
		}
		manifest, err := pluginmgr.ValidateSource(local)
		if err != nil {
			return "", pluginmgr.ResolvedPackage{}, err
		}
		return local, pluginmgr.ResolvedPackage{Name: manifest.ID, Version: manifest.Version, Tarball: spec}, nil
	default:
		return "", pluginmgr.ResolvedPackage{}, fmt.Errorf("不是 registry/tarball 规格: %q（kind=%s）", spec, kind)
	}
}

func registryInspectionRefused(spec string, err error) transport.OfficialPluginSpecInspection {
	return transport.OfficialInspectionRefused("not-a-package",
		fmt.Sprintf("registry/tarball 解析失败（%s）：%v", strings.TrimSpace(spec), err))
}

func registryChangeRefused(spec, code string, err error) transport.OfficialChangeResult {
	return transport.OfficialFailedChange("install", spec, code, err.Error())
}
