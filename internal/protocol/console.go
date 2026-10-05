package protocol

import (
	"bufio"
	"fmt"
	"io"
)

// consoleReader implements bounded line input and basic Telnet negotiation.
// It accepts both CR and CRLF and echoes interactive SSH/Telnet input itself.
type consoleReader struct {
	r      *bufio.Reader
	w      io.Writer
	telnet bool
	skipLF bool
}

func (c *consoleReader) readLine(echo bool) (string, error) {
	line := make([]byte, 0, 128)
	for {
		b, err := c.r.ReadByte()
		if err != nil {
			return "", err
		}
		if c.telnet && b == 255 {
			cmd, err := c.r.ReadByte()
			if err != nil {
				return "", err
			}
			switch cmd {
			case 251, 252, 253, 254:
				option, err := c.r.ReadByte()
				if err != nil {
					return "", err
				}
				if cmd == 251 {
					_, _ = c.w.Write([]byte{255, 254, option})
				}
				if cmd == 253 && option != 1 && option != 3 {
					_, _ = c.w.Write([]byte{255, 252, option})
				}
				continue
			case 250:
				previous := byte(0)
				for n := 0; ; n++ {
					if n > 1024 {
						return "", fmt.Errorf("Telnet negotiation too long")
					}
					b, err = c.r.ReadByte()
					if err != nil {
						return "", err
					}
					if previous == 255 && b == 240 {
						break
					}
					previous = b
				}
				continue
			case 255:
				b = 255
			default:
				continue
			}
		}
		if c.skipLF {
			c.skipLF = false
			if b == '\n' || b == 0 {
				continue
			}
		}
		switch b {
		case '\r', '\n':
			c.skipLF = b == '\r'
			_, _ = io.WriteString(c.w, "\r\n")
			return string(line), nil
		case 8, 127:
			if len(line) > 0 {
				line = line[:len(line)-1]
				if echo {
					_, _ = io.WriteString(c.w, "\b \b")
				}
			}
		case 3:
			_, _ = io.WriteString(c.w, "^C\r\n")
			return "", nil
		case 4:
			return "", io.EOF
		case 27:
			// Ignore a short ANSI cursor-key sequence; it must not become a command.
			for i := 0; i < 8; i++ {
				b, err = c.r.ReadByte()
				if err != nil {
					return "", err
				}
				if b >= 64 && b <= 126 && b != '[' {
					break
				}
			}
		default:
			if b < 32 {
				continue
			}
			if len(line) >= 8192 {
				return "", fmt.Errorf("CLI line exceeds 8192 bytes")
			}
			line = append(line, b)
			if echo {
				_, _ = c.w.Write([]byte{b})
			}
		}
	}
}
