// Package proto define as mensagens de sinalização trocadas via WebSocket
// entre agente, servidor e visualizador.
package proto

import "encoding/json"

// Msg é o envelope único de sinalização.
//
//	agente  -> servidor : register{id,key}
//	servidor -> agente  : registered{id} | incoming{sid,ice,msg=ip} | signal | bye | error
//	viewer  -> servidor : connect{id}
//	servidor -> viewer  : session{sid,ice} | signal | bye | error
//	ambos   -> servidor : signal{sid,data} | bye{sid}
type Msg struct {
	T    string          `json:"t"`
	ID   string          `json:"id,omitempty"`
	Key  string          `json:"key,omitempty"`
	SID  string          `json:"sid,omitempty"`
	Data json.RawMessage `json:"data,omitempty"`
	ICE  []ICEServer     `json:"ice,omitempty"`
	Msg  string          `json:"msg,omitempty"`
}

// ICEServer segue o formato RTCIceServer do navegador.
type ICEServer struct {
	URLs       []string `json:"urls"`
	Username   string   `json:"username,omitempty"`
	Credential string   `json:"credential,omitempty"`
}
