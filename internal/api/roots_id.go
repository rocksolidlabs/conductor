package api

import "os"

// folderID is what a folder is: its device and number (os.SameFile), its
// owner, and its birth time where the system gives one. A folder made later
// at the same path is another folder, whatever number it is given.
type folderID struct {
	fi    os.FileInfo
	uid   int   // -1 where the system gives none
	birth int64 // nanoseconds since the epoch; 0 where the system gives none
}

// identify is what the folder at path, fi, is.
func identify(path string, fi os.FileInfo) folderID {
	if fi == nil {
		return folderID{uid: -1}
	}
	return folderID{fi: fi, uid: folderOwner(fi), birth: birthOf(path, fi)}
}

// known reports whether id is a folder's (not the empty one).
func (id folderID) known() bool { return id.fi != nil }

// same reports whether a and b are one folder: the same device and number,
// owner and, where both have one, birth time.
func (a folderID) same(b folderID) bool {
	if a.fi == nil || b.fi == nil || !os.SameFile(a.fi, b.fi) || a.uid != b.uid {
		return false
	}
	return a.birth == 0 || b.birth == 0 || a.birth == b.birth
}

// sameStat reports whether got, a handle's stat of a folder, is id as far
// as such a stat says: the same device and number, and owner.
func (id folderID) sameStat(got os.FileInfo) bool {
	return id.fi != nil && got != nil && os.SameFile(id.fi, got) && id.uid == folderOwner(got)
}

// ownedByServer reports whether the server's own user owns the folder id
// is (a system that names no owner: yes).
func ownedByServer(id folderID) bool {
	return id.uid < 0 || id.uid == serverUID()
}
