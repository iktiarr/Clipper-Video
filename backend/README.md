# Video Clipper Machine – Backend

Backend Go untuk Video Clipper Machine.

## Cara Install FFmpeg

### Windows
```powershell
# Menggunakan winget
winget install ffmpeg

# Atau menggunakan Chocolatey
choco install ffmpeg
```

Setelah install, restart terminal dan cek dengan:
```powershell
ffmpeg -version
```

### Ubuntu / Debian
```bash
sudo apt update && sudo apt install ffmpeg
```

### macOS
```bash
brew install ffmpeg
```

## Cara Menjalankan Backend

```bash
cd backend
go mod tidy
go run main.go
```

Server akan berjalan di: **http://localhost:8080**

## Endpoint API

| Method | Path | Deskripsi |
|--------|------|-----------|
| POST | `/api/upload` | Upload video + mulai proses potong |
| GET | `/api/jobs/{jobId}` | Cek status & daftar clip |
| GET | `/api/jobs/{jobId}/clips/{filename}` | Download clip |
| DELETE | `/api/jobs/{jobId}` | Hapus job & semua file |
| GET | `/health` | Health check |

## Catatan Penting

- File hanya bersifat **sementara** – dihapus otomatis setelah 2 jam
- File input dihapus segera setelah proses FFmpeg selesai
- Cleanup berjalan setiap 30 menit
- Ukuran upload maksimal: **5 GB**
- Kualitas video **tetap asli** karena menggunakan `-c copy` (tidak re-encode)
