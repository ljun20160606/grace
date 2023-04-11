package grace

import (
	"encoding/binary"
	"encoding/json"
	"io"
)

type NetGraceProtocol struct {
	Data interface{}
}

func (n *NetGraceProtocol) WriteTo(w io.Writer) (int64, error) {
	dataBytes, err := json.Marshal(n.Data)
	if err != nil {
		return 0, err
	}
	lengthBytes := make([]byte, 4)
	binary.LittleEndian.PutUint32(lengthBytes, uint32(len(dataBytes)))

	nn, err := w.Write(append(lengthBytes, dataBytes...))
	return int64(nn), err
}

func (n *NetGraceProtocol) ReadFrom(r io.Reader) (int64, error) {
	lengthBuf := make([]byte, 4)
	if nn, err := r.Read(lengthBuf); err != nil {
		return int64(nn), err
	}

	length := binary.LittleEndian.Uint32(lengthBuf)
	dataBuf := make([]byte, length)
	if nn, err := r.Read(dataBuf); err != nil {
		return 0, err
	} else {
		if err := json.Unmarshal(dataBuf, n.Data); err != nil {
			return 0, err
		}
		return int64(nn), nil
	}
}
