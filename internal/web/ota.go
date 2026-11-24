package web

import (
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"
)

// ValidateOTABinary verifies ELF header, endianness, and target CPU architecture before applying an OTA binary.
func ValidateOTABinary(data []byte, expectedArch string, minSize int) error {
	if minSize <= 0 {
		minSize = 1 << 20
	}
	if len(data) < minSize {
		return fmt.Errorf("binary too small (%d bytes, minimum %d bytes)", len(data), minSize)
	}
	if len(data) < 20 {
		return fmt.Errorf("invalid binary header")
	}
	if data[0] != 0x7f || data[1] != 'E' || data[2] != 'L' || data[3] != 'F' {
		return fmt.Errorf("not a Linux ELF binary (expected compiled kindleiot binary, not .zip/.sh)")
	}
	if data[5] != 1 {
		return fmt.Errorf("unsupported ELF endianness %d (expected little-endian)", data[5])
	}

	arch := expectedArch
	if arch == "" {
		arch = runtime.GOARCH
	}
	elfClass := data[4] // 1 = 32-bit, 2 = 64-bit
	machine := binary.LittleEndian.Uint16(data[18:20])

	switch arch {
	case "arm":
		if elfClass != 1 || machine != 40 {
			return fmt.Errorf("wrong architecture (class=%d machine=%d): Kindle requires Linux ARMv7 ELF32 (kindleiot-arm)", elfClass, machine)
		}
	case "amd64":
		if elfClass != 2 || machine != 62 {
			return fmt.Errorf("wrong architecture (class=%d machine=%d): host requires Linux amd64 ELF64", elfClass, machine)
		}
	case "arm64":
		if elfClass != 2 || machine != 183 {
			return fmt.Errorf("wrong architecture (class=%d machine=%d): host requires Linux arm64 ELF64", elfClass, machine)
		}
	}
	return nil
}

// WriteStagedBinary writes the validated OTA binary beside exePath as <exePath>.new with mode 0755.
func WriteStagedBinary(exePath string, data []byte) (string, error) {
	staged := exePath + ".new"
	f, err := os.OpenFile(staged, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return "", fmt.Errorf("create staged binary %s: %w", staged, err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(staged)
		return "", fmt.Errorf("write staged binary: %w", err)
	}
	_ = f.Sync()
	if err := f.Close(); err != nil {
		return "", err
	}
	_ = os.Chmod(staged, 0o755)
	return staged, nil
}

// SpawnOTARestart launches a detached helper shell script that stops the current process cleanly,
// swaps stagedPath into exePath (avoiding ETXTBSY/FAT32 rename locks), and restarts KindleIOT.
func SpawnOTARestart(exePath, stagedPath string, origArgs []string) error {
	dir := filepath.Dir(exePath)
	script := `
PID="$1"
STAGED="$2"
EXE="$3"
DIR="$4"
shift 4
sleep 1
kill "$PID" 2>/dev/null || true
for _ in 1 2 3 4 5 6 7 8 9 10 11 12; do
  if ! kill -0 "$PID" 2>/dev/null; then
    break
  fi
  sleep 0.5
done
if kill -0 "$PID" 2>/dev/null; then
  kill -9 "$PID" 2>/dev/null || true
  sleep 0.5
fi
cp -f "$EXE" "$EXE.bak" 2>/dev/null || true
mv -f "$STAGED" "$EXE"
chmod +x "$EXE"
if [ -f "$DIR/start.sh" ]; then
  nohup /bin/sh "$DIR/start.sh" >/dev/null 2>&1 </dev/null &
else
  export GODEBUG="asyncpreemptoff=1"
  nohup "$EXE" "$@" >/dev/null 2>&1 </dev/null &
fi
`
	args := make([]string, 0, 7+len(origArgs))
	args = append(args, "-c", script, "kindleiot-ota", strconv.Itoa(os.Getpid()), stagedPath, exePath, dir)
	args = append(args, origArgs...)

	cmd := exec.Command("/bin/sh", args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0); err == nil {
		cmd.Stdin = devNull
		cmd.Stdout = devNull
		cmd.Stderr = devNull
		defer devNull.Close()
	}
	return cmd.Start()
}
