package library

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"time"
)

const (
	ModeLink = "link" // hardlink; the download keeps seeding
	ModeMove = "move" // link then unlink the source; for imports
)

// Placed is one completed file operation.
type Placed struct {
	Src  string `json:"src"`
	Dst  string `json:"dst"`
	Mode string `json:"mode"`
}

// Receipt records a filing so it can be undone.
type Receipt struct {
	ID       string     `json:"id"`
	Time     time.Time  `json:"time"`
	ItemID   string     `json:"item_id"`
	Name     string     `json:"name"`
	Torrent  bool       `json:"torrent"`
	Hash     string     `json:"hash,omitempty"`
	Kind     string     `json:"kind"`
	Title    string     `json:"title"`
	Year     string     `json:"year"`
	Placed   []Placed   `json:"placed"`
	Dirs     []string   `json:"dirs"` // directories filing created, outermost first
	UndoneAt *time.Time `json:"undone_at,omitempty"`
}

// Execute performs the plan's operations. Every destination is created without replacing
// anything; on failure the operations already done are reversed.
func Execute(p Plan, mode string) (Receipt, error) {
	r := Receipt{
		ItemID: p.Item.ID, Name: p.Item.Name, Torrent: p.Item.Torrent, Hash: p.Item.Hash,
		Kind: p.Edit.Kind, Title: p.Edit.Title, Year: p.Edit.Year, Time: time.Now(),
	}
	if p.Error != "" {
		return r, errors.New(p.Error)
	}
	for _, op := range p.Ops {
		info, err := os.Lstat(op.Src)
		if err != nil {
			return r, fmt.Errorf("source %s: %w", filepath.Base(op.Src), err)
		}
		if !info.Mode().IsRegular() {
			return r, fmt.Errorf("source %s is not a regular file", filepath.Base(op.Src))
		}
	}
	for _, op := range p.Ops {
		dirs, err := mkdirs(filepath.Dir(op.Dst))
		r.Dirs = append(r.Dirs, dirs...)
		if err == nil {
			err = place(op.Src, op.Dst, mode)
		}
		if err != nil {
			rollback(&r)
			return r, fmt.Errorf("%s: %w", filepath.Base(op.Dst), linkError(err))
		}
		r.Placed = append(r.Placed, Placed{op.Src, op.Dst, mode})
	}
	return r, nil
}

// place never replaces an existing destination, so a racing file is never overwritten.
func place(src, dst, mode string) error {
	if mode == ModeMove {
		return renameNoReplace(src, dst)
	}
	return os.Link(src, dst)
}

func linkError(err error) error {
	var le *os.LinkError
	if errors.As(err, &le) {
		switch {
		case errors.Is(le.Err, fs.ErrExist):
			return errors.New("already exists in the library")
		case isCrossDevice(le.Err):
			return errors.New("the download and the library are on different filesystems, so the file cannot be hardlinked")
		case errors.Is(le.Err, fs.ErrPermission):
			return errors.New("permission denied; the organizer needs write access to the source file and the library")
		}
	}
	return err
}

// mkdirs creates dir and its missing parents, returning the ones it created, outermost first.
func mkdirs(dir string) ([]string, error) {
	var missing []string
	for d := dir; ; d = filepath.Dir(d) {
		if _, err := os.Lstat(d); err == nil {
			break
		} else if !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		missing = append(missing, d)
		if filepath.Dir(d) == d {
			break
		}
	}
	slices.Reverse(missing)
	for i, d := range missing {
		if err := os.Mkdir(d, 0o775); err != nil && !errors.Is(err, fs.ErrExist) {
			return missing[:i], err
		}
	}
	return missing, nil
}

func rollback(r *Receipt) {
	for i := len(r.Placed) - 1; i >= 0; i-- {
		_ = reverse(r.Placed[i])
	}
	r.Placed = nil
	removeEmpty(r.Dirs)
}

func removeEmpty(dirs []string) {
	for i := len(dirs) - 1; i >= 0; i-- {
		_ = RemoveEmptyDir(dirs[i])
	}
}

// ErrUndo explains why a filing cannot be undone safely.
var ErrUndo = errors.New("cannot undo")

// Undo reverses a receipt. It checks every file first and changes nothing when any
// step would lose data, such as deleting the library copy of a download that is gone.
func Undo(r Receipt) error {
	if r.UndoneAt != nil {
		return fmt.Errorf("%w: already undone", ErrUndo)
	}
	for _, p := range r.Placed {
		if err := checkReverse(p); err != nil {
			return err
		}
	}
	for i := len(r.Placed) - 1; i >= 0; i-- {
		if err := reverse(r.Placed[i]); err != nil {
			return fmt.Errorf("%s: %w", filepath.Base(r.Placed[i].Dst), err)
		}
	}
	removeEmpty(r.Dirs)
	return nil
}

func checkReverse(p Placed) error {
	dst, dstErr := os.Lstat(p.Dst)
	src, srcErr := os.Lstat(p.Src)
	name := filepath.Base(p.Dst)
	switch p.Mode {
	case ModeLink:
		if errors.Is(dstErr, fs.ErrNotExist) {
			return nil
		}
		if errors.Is(srcErr, fs.ErrNotExist) {
			return fmt.Errorf("%w: the download of %s is gone, so the library copy is the only one", ErrUndo, name)
		}
		if dstErr != nil || srcErr != nil || !os.SameFile(src, dst) {
			return fmt.Errorf("%w: %s changed since it was filed", ErrUndo, name)
		}
	case ModeMove:
		if dstErr != nil {
			return fmt.Errorf("%w: %s is no longer in the library", ErrUndo, name)
		}
		if !errors.Is(srcErr, fs.ErrNotExist) {
			return fmt.Errorf("%w: something is back at the original place of %s", ErrUndo, name)
		}
	default:
		return fmt.Errorf("%w: unknown mode %q", ErrUndo, p.Mode)
	}
	return nil
}

func reverse(p Placed) error {
	switch p.Mode {
	case ModeLink:
		err := os.Remove(p.Dst)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	case ModeMove:
		if _, err := mkdirs(filepath.Dir(p.Src)); err != nil {
			return err
		}
		return renameNoReplace(p.Dst, p.Src)
	}
	return nil
}
