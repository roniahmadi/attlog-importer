# attlog-importer

Aplikasi desktop (Go + [Fyne](https://fyne.io)) untuk mengirim file attlog mesin fingerprint
(format ZKTeco/iclock) ke endpoint `/iclock/cdata` sistem ASIK, dalam batch dengan jeda antar request.

## Fitur

- Pilih file `*_attlog.dat`; serial mesin diambil otomatis dari nama file.
- Baris dengan PIN non-numerik (mis. `All`) dilewati.
- Mode dry run aktif secara default. Mengirim butuh konfirmasi.
- Log menampilkan status dan header serta body respons HTTP per batch.
- Tombol Hentikan dan opsi "Mulai dari batch" untuk melanjutkan.

## Build

Linux membutuhkan paket dev untuk Fyne:

```bash
sudo apt install libgl1-mesa-dev xorg-dev
```

Lalu:

```bash
go build -o attlog-importer .
```

Jika `libwayland-dev` belum terpasang, build dengan tag X11:

```bash
go build -tags x11 -o attlog-importer .
```

## Catatan

- Request memakai User-Agent `iClock Proxy/1.09`, karena server ASIK hanya menerima request dengan string itu.
- Jalur HTTP memproses setiap baris lewat logika mesin live di server. Gunakan dry run dulu.
