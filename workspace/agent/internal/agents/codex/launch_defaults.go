package codex

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/paths"
	"github.com/pelletier/go-toml/v2/unstable"
)

// RememberLaunchDefaults reads the workspace preference through the Agent's settings layer.
var RememberLaunchDefaults = func() bool { return true }

func prepareFreshLaunch() error {
	if RememberLaunchDefaults() {
		return nil
	}
	paths.CodexConfigMu.Lock()
	defer paths.CodexConfigMu.Unlock()
	if err := clearSavedLaunchDefaults(codexConfigPath()); err != nil {
		return fmt.Errorf("Codex のモデルと推論レベルの保存設定を削除できませんでした: %w", err)
	}
	return nil
}

func clearSavedLaunchDefaults(path string) error {
	// Preserve the workspace's config symlink when replacing the file atomically.
	target, err := filepath.EvalSymlinks(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	info, err := os.Stat(target)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("Codex の設定が通常のファイルではありません")
	}
	before, err := os.ReadFile(target)
	if err != nil {
		return err
	}
	after, err := stripSavedLaunchDefaults(before)
	if err != nil || bytes.Equal(after, before) {
		return err
	}
	current, err := os.ReadFile(target)
	if err != nil {
		return err
	}
	if !bytes.Equal(current, before) {
		return errors.New("Codex の設定が変更されました。セッションをもう一度起動してください")
	}
	return agents.WriteFileAtomic(target, after, info.Mode().Perm())
}

// Only the two root assignments belong to this preference. Quoted keys and
// multiline strings require parsing so unrelated tables and their text survive.
func stripSavedLaunchDefaults(src []byte) ([]byte, error) {
	var parser unstable.Parser
	parser.Reset(src)
	var out []byte
	var next uint32
	root := true
	for parser.NextExpression() {
		expr := parser.Expression()
		if expr.Kind == unstable.Table || expr.Kind == unstable.ArrayTable {
			root = false
		}
		if !root || expr.Kind != unstable.KeyValue {
			continue
		}
		keys := expr.Key()
		keys.Next()
		key := keys.Node()
		start := key.Raw.Offset
		name := string(key.Data)
		if keys.Next() || (name != "model" && name != "model_reasoning_effort") {
			continue
		}
		value := expr.Value()
		if value.Kind != unstable.String {
			return nil, errors.New("Codex のモデル設定が文字列ではありません")
		}
		out = append(out, src[next:start]...)
		next = value.Raw.Offset + value.Raw.Length
	}
	if parser.Error() != nil {
		return nil, errors.New("Codex の設定を TOML として読み取れませんでした")
	}
	if next == 0 {
		return src, nil
	}
	return append(out, src[next:]...), nil
}
