package packfile

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"time"

	"github.com/PlakarLabs/plakar/logger"
	"github.com/PlakarLabs/plakar/profiler"
)

const VERSION = 100

const (
	TYPE_CHUNK     = 1
	TYPE_OBJECT    = 2
	TYPE_CONTAINER = 3
	TYPE_BINARY    = 4
)

type Blob struct {
	Type     uint8
	Checksum [32]byte
	Offset   uint32
	Length   uint32
}

type PackFile struct {
	Data  []byte
	Index []Blob

	//Metadata PackFileMetadata  // Metadata for the packfile
	//Footer   PackFileFooter    // Footer information for quick lookup and integrity verification
}

// PackFileMetadata stores metadata about the packfile itself
type PackFileMetadata struct {
	Version      uint32
	CreationTime time.Time
	Count        uint32
}

// PackFileFooter stores footer information for quick lookup and integrity checks
type PackFileFooter struct {
	IndexOffset      uint32
	IndexChecksum    [32]byte
	MetadataOffset   uint32
	MetadataChecksum [32]byte
}

func New() *PackFile {
	return &PackFile{
		Data:  make([]byte, 0),
		Index: make([]Blob, 0),
	}
}

func NewFromBytes(serialized []byte) (*PackFile, error) {
	t0 := time.Now()
	defer func() {
		profiler.RecordEvent("packfile.NewFromBytes", time.Since(t0))
		logger.Trace("packfile", "NewFromBytes(...): %s", time.Since(t0))
	}()

	reader := bytes.NewReader(serialized)

	var totalLength uint32
	_, err := reader.Seek(-4, io.SeekEnd)
	if err != nil {
		return nil, err
	}
	if err := binary.Read(reader, binary.LittleEndian, &totalLength); err != nil {
		return nil, err
	}

	_, err = reader.Seek(0, io.SeekStart)
	if err != nil {
		return nil, err
	}
	data := make([]byte, totalLength)
	if err := binary.Read(reader, binary.LittleEndian, &data); err != nil {
		return nil, err
	}

	// we won't read the totalLength again
	remaining := reader.Len() - 4

	p := New()
	p.Data = data
	for remaining > 0 {
		var dataType uint8
		var checksum [32]byte
		var chunkOffset uint32
		var chunkLength uint32
		if err := binary.Read(reader, binary.LittleEndian, &dataType); err != nil {
			return nil, err
		}
		if err := binary.Read(reader, binary.LittleEndian, &checksum); err != nil {
			return nil, err
		}
		if err := binary.Read(reader, binary.LittleEndian, &chunkOffset); err != nil {
			return nil, err
		}
		if err := binary.Read(reader, binary.LittleEndian, &chunkLength); err != nil {
			return nil, err
		}

		if chunkOffset+chunkLength > totalLength {
			return nil, fmt.Errorf("chunk offset + chunk length exceeds total length of packfile")
		}

		p.Index = append(p.Index, Blob{
			Type:     dataType,
			Checksum: checksum,
			Offset:   chunkOffset,
			Length:   chunkLength,
		})
		remaining -= (len(checksum) + 9)
	}
	return p, nil
}

func (p *PackFile) Serialize() ([]byte, error) {
	t0 := time.Now()
	defer func() {
		profiler.RecordEvent("packfile.Serialize", time.Since(t0))
		logger.Trace("packfile", "Serialize(): %s", time.Since(t0))
	}()

	var buffer bytes.Buffer
	if err := binary.Write(&buffer, binary.LittleEndian, p.Data); err != nil {
		return nil, err
	}
	for _, chunk := range p.Index {
		if err := binary.Write(&buffer, binary.LittleEndian, chunk.Type); err != nil {
			return nil, err
		}
		if err := binary.Write(&buffer, binary.LittleEndian, chunk.Checksum); err != nil {
			return nil, err
		}
		if err := binary.Write(&buffer, binary.LittleEndian, chunk.Offset); err != nil {
			return nil, err
		}
		if err := binary.Write(&buffer, binary.LittleEndian, chunk.Length); err != nil {
			return nil, err
		}
	}
	totalLength := uint32(len(p.Data))
	if err := binary.Write(&buffer, binary.LittleEndian, totalLength); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func (p *PackFile) AddBlob(dataType uint8, checksum [32]byte, data []byte) {
	t0 := time.Now()
	defer func() {
		profiler.RecordEvent("packfile.AddChunk", time.Since(t0))
		logger.Trace("packfile", "AddChunk(...): %s", time.Since(t0))
	}()
	p.Index = append(p.Index, Blob{dataType, checksum, uint32(len(p.Data)), uint32(len(data))})
	p.Data = append(p.Data, data...)

}

func (p *PackFile) GetBlob(checksum [32]byte) ([]byte, bool) {
	t0 := time.Now()
	defer func() {
		profiler.RecordEvent("packfile.GetChunk", time.Since(t0))
		logger.Trace("packfile", "GetChunk(...): %s", time.Since(t0))
	}()

	for _, chunk := range p.Index {
		if chunk.Checksum == checksum {
			return p.Data[chunk.Offset : chunk.Offset+chunk.Length], true
		}
	}
	return nil, false
}

func (p *PackFile) Size() uint32 {
	return uint32(len(p.Data))
}
