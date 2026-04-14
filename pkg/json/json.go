package json

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/kaptinlin/jsonrepair"

	prov "github.com/bytedance/sonic"
)

// RawMessage re-exports encoding/json.RawMessage so callers don't need to import encoding/json directly.
type RawMessage = json.RawMessage

// RepairAndUnmarshal 对 LLM 返回的结构化 JSON 做 repair + 反序列化。
// 统一收拢 Service/Runtime 层中 jsonrepair.Repair → json.Unmarshal 的重复模式。
func RepairAndUnmarshal[T any](raw string, out *T) error {
	repaired, err := jsonrepair.Repair(raw)
	if err != nil {
		return fmt.Errorf("repair json: %w", err)
	}
	if err := prov.UnmarshalString(repaired, out); err != nil {
		return fmt.Errorf("unmarshal json: %w", err)
	}
	return nil
}

func Unmarshal(data []byte, v any) error {
	return prov.Unmarshal(data, v)
}

func UnmarshalString(data string, v any) error {
	return prov.UnmarshalString(data, v)
}

func MarshalIndent(v any, prefix, indent string) ([]byte, error) {
	return prov.MarshalIndent(v, prefix, indent)
}

func Marshal(v any) ([]byte, error) {
	return prov.Marshal(v)
}

func MarshalString(v any) (string, error) {
	return prov.MarshalString(v)
}

func NewEncoder(w io.Writer) prov.Encoder {
	return prov.ConfigDefault.NewEncoder(w)
}

func NewDecoder(r io.Reader) prov.Decoder {
	return prov.ConfigDefault.NewDecoder(r)
}

func MarshalFast(v any) ([]byte, error) {
	return prov.ConfigFastest.Marshal(v)
}

func UnmarshalFast(data []byte, v any) error {
	return prov.ConfigFastest.Unmarshal(data, v)
}
