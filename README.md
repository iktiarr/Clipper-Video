# 🎬 Video Clipper Machine

Website untuk memotong video secara otomatis berdasarkan durasi yang dipilih.

## Stack

- **Frontend**: SvelteKit + Vanilla CSS
- **Backend**: Golang
- **Video Processing**: FFmpeg (server-side, `-c copy` mode)

---

## Persyaratan

1. [Go 1.21+](https://go.dev/dl/)
2. [Node.js 18+](https://nodejs.org/)
3. [FFmpeg](https://ffmpeg.org/download.html)

### Install FFmpeg

**Windows (winget):**
```powershell
winget install ffmpeg
```

**Windows (Chocolatey):**
```powershell
choco install ffmpeg
```

**Ubuntu/Debian:**
```bash
sudo apt update && sudo apt install ffmpeg
```

**macOS:**
```bash
brew install ffmpeg
```

Verifikasi: `ffmpeg -version`

---

## Cara Menjalankan

### Backend (Go)

```bash
cd backend
go mod tidy
go run main.go
```

Backend berjalan di: **http://localhost:8080**

### Frontend (SvelteKit)

```bash
cd frontend
npm install
npm run dev
```

Frontend berjalan di: **http://localhost:5173**

---

## Cara Menggunakan

1. Buka browser ke `http://localhost:5173`
2. Pilih file video (maksimal 5 GB)
3. Pilih durasi potongan: 1, 2, 5, 10 menit, atau custom
4. Klik **"Upload dan Potong Video"**
5. Tunggu proses selesai
6. Download hasil potongan satu per satu

---

## Struktur Folder

```
clipper-video/
├── backend/
│   ├── main.go          # Server Go + endpoint API
│   ├── go.mod
│   ├── go.sum
│   ├── temp/
│   │   ├── uploads/     # File upload sementara (dihapus setelah FFmpeg)
│   │   └── outputs/     # Hasil clip (dihapus otomatis setelah 2 jam)
│   └── README.md
├── frontend/
│   ├── src/
│   │   ├── routes/
│   │   │   ├── +layout.svelte
│   │   │   └── +page.svelte   # Halaman utama
│   │   └── app.css
│   ├── .env             # VITE_API_URL
│   └── package.json
└── README.md
```

---

## Endpoint API Backend

| Method | Path | Deskripsi |
|--------|------|-----------|
| `POST` | `/api/upload` | Upload video + mulai potong |
| `GET` | `/api/jobs/{jobId}` | Cek status & daftar clip |
| `GET` | `/api/jobs/{jobId}/clips/{file}` | Download clip |
| `DELETE` | `/api/jobs/{jobId}` | Hapus job & file |

---

## Catatan Penting

- ⚠️ **File bersifat sementara** — file input dihapus segera setelah FFmpeg selesai, file output dihapus otomatis setelah 2 jam
- 🎯 **Kualitas video tetap asli** — menggunakan `-c copy` sehingga tidak ada re-encoding
- 📏 **Presisi keyframe** — karena mode copy mengikuti keyframe, potongan mungkin tidak 100% presisi ke detik, tetapi ini normal dan tidak mempengaruhi kualitas
- 🔒 **Upload maksimal 5 GB**

## FFmpeg Command yang Digunakan

```bash
ffmpeg -y -i input.ext -map 0 -c copy -f segment -segment_time <detik> -reset_timestamps 1 clip_%03d.ext
```
