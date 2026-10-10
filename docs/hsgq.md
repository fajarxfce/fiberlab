# HSGQ untuk aplikasi FTTH

OLT Fiberlab melayani dua tabel SNMPv2c dari model ONU yang sama: subset GPON
HSGQ-G01ID dan tabel kompatibilitas EPON HSGQ-E04I. Adapter `HsgqEponSnmpAdapter`
di aplikasi FTTH bisa membaca daftar ONU, PON, status serta RX/TX tanpa perubahan
aplikasi. Keduanya berjalan di agent Go yang sudah ada; tidak ada container,
daemon SNMP atau Java tambahan untuk menjalankan lab.

## Menghubungkan aplikasi

1. Jalankan lab dan buka **Integration**.
2. Di aplikasi FTTH, tambahkan OLT dengan vendor **HSGQ**, alamat management OLT
   (OLT pertama biasanya **10.203.0.64**), SNMP **v2c**, port **161**, dan community
   yang ditampilkan di Integration (baku **lab-read**).
3. Di bagian **ONU identities for FTTH**, salin **FTTH identity (MAC)** untuk
   identitas/serial ONU pelanggan di adapter HSGQ EPON, misalnya `024654000001`.
   Kolom **GPON / ACS serial**, misalnya `HSGQ00000001`, dipakai untuk tabel GPON
   dan ACS; adapter EPON aplikasi memakai MAC.
4. Hubungkan ONU ke pelanggan/ODP di aplikasi, lalu jalankan polling SNMP.
   Matikan ONU, potong kabel, atau tambahkan redaman di Fiberlab untuk menguji
   perubahan status dan alarm. Perbaikan mengembalikan bacaan optik.

IP management harus dapat dicapai dari proses yang menjalankan polling. Domain
Cloudflare melayani UI/API HTTP; polling SNMP memakai UDP langsung ke IP OLT.
Tidak perlu mengganti community simulator menjadi community perangkat asli.
Lab lama otomatis mendapat tabel ini setelah runtime memakai binary terbaru.

`GET /api/v1/labs/ID/connections` dan **Export connection details** juga memuat
`onus`: ID/nama, OLT/PON, `snmpIndex` EPON, `gponIndex`, serial, MAC dan
`ftthIdentity`. MAC yang kosong dalam dokumen lama diturunkan secara deterministik
dari ID/serial ONU. Alamat yang sama digunakan oleh klien Ethernet dan SNMP,
sehingga identitas tidak berubah setiap Run.

## Kolom yang didukung

Semua prefix di bawah diawali `.1.3.6.1.4.1.50224`.

| Data | GPON G01ID | EPON kompatibilitas E04I | Tipe / nilai |
| --- | --- | --- | --- |
| Nama ONU | `.3.12.2.1.2.I` | `.3.3.2.1.2.I` | OCTET STRING, `ONT01/000` / `ONU01/01` |
| Identitas ONU | `.3.12.2.1.15.I` | `.3.3.2.1.7.I` | Serial ASCII 12 karakter / MAC 6 byte mentah |
| Status ONU | `.3.12.2.1.4.I` | `.3.3.2.1.8.I` | INTEGER: 1 online, 2 offline |
| RX ONU | `.3.12.3.1.4.I.0.0` | `.3.3.3.1.4.I.0.0` | INTEGER, 0,01 dBm |
| TX ONU | `.3.12.3.1.5.I.0.0` | `.3.3.3.1.5.I.0.0` | INTEGER, 0,01 dBm |

Indeks `I` memakai `0x0100PPNN`: PP nomor PON; NN nomor ONU. GPON mulai 0,
EPON mulai 1. Contoh ONU pertama di PON1: GPON **16777472**, EPON **16777473**.
Nomor diurutkan dari ID ONU dalam setiap OLT/PON, maksimal 128 ONU per PON dan
500 dalam satu lab. Power, kabel, billing, urutan node di dokumen dan restart
tidak mengubah indeks. Mengubah topologi/anggota PON dapat mengubah nomor;
MAC/serial tetap menjadi identitas ONU.

Inventori memuat semua ONU yang terhubung dalam topologi, termasuk saat offline.
ONU mati, LOS, PON down, disabled atau belum diotorisasi memakai status 2 dan
tidak memiliki baris optik. Tidak ada angka 0 dBm atau bacaan lama untuk
menggantikan sinyal yang hilang. Mempertahankan ONU belum diotorisasi sebagai
offline merupakan aturan kompatibilitas simulator; tabel discovery vendor
belum diimplementasikan.

Suspend billing atau uplink Ethernet putus tidak membuat LOS: status optik dan
redaman tetap tersedia. Sesi PPP diperiksa melalui RouterOS/RADIUS dan
FTTHLAB-MIB. RX berasal dari perhitungan kabel/splitter; TX dari konfigurasi ONU.
Perubahan SNMP terlihat paling lambat setelah cache snapshot 500 ms diperbarui.

~~~sh
# Tabel yang dibaca adapter HSGQ aplikasi FTTH
snmpbulkwalk -v2c -c lab-read 10.203.0.64 .1.3.6.1.4.1.50224.3.3
# Format GPON dari referensi G01ID
snmpbulkwalk -v2c -c lab-read 10.203.0.64 .1.3.6.1.4.1.50224.3.12
~~~

## Referensi dan batas kompatibilitas

Subset GPON berasal dari pembacaan HSGQ-G01ID pada 2026-10-10. Tabel EPON
berasal dari fixture E04I dan adapter dalam source FTTH. Detail asal dan data
yang dianonimkan ada di [fixture HSGQ](../internal/protocol/testdata/hsgq/README.md).
**OLT G01ID asli tidak melayani tabel EPON ini.** Kompatibilitas Fiberlab dengan
adapter FTTH EPON tidak berarti adapter tersebut sudah bisa membaca G01ID asli.

Fiberlab tetap memiliki 8 PON virtual dan 1 uplink di IF-MIB. Unit G01ID yang
dibaca memiliki PON01, GE01–GE04, XGE01; layout, kecepatan dan counter perangkat
itu tidak diklon. `sysDescr` menyebut profil simulasi dan `sysObjectID` tetap
enterprise eksperimental FTTHLAB-MIB. Ini bukan emulasi seluruh firmware G01ID.

OID lainnya, uptime/jarak/penyebab down vendor, tabel transceiver OLT
`.65535.65535`, vendor traps, provisioning SNMP SET dan grammar CLI HSGQ belum
didukung. Unknown OID memakai exception SNMP; SET mengembalikan `notWritable`.
Kontrol ONU dilakukan lewat UI, API atau CLI `lab ...` yang terdokumentasi.

## Verifikasi terhadap adapter FTTH

Tes Go standar memeriksa fixture wire GPON/EPON, tipe ASN.1, isolasi antar-OLT,
gangguan dan pemulihan, serta walk UDP 500 ONU. Tes tambahan berikut mengekstrak
adapter dan SNMP4J dari boot JAR FTTH, lalu menjalankan polling asli lewat UDP
ke agent Fiberlab sementara. Tidak perlu menjalankan server/database FTTH.

~~~sh
FTTH_BOOT_JAR=/path/to/ftth/server/build/libs/server-0.1.0-SNAPSHOT.jar \
  go test -race ./internal/protocol -run TestFTTHHSGQAdapterInterop -v
~~~

JDK 21 diperlukan hanya untuk probe pengujian. Tes ini mencakup 500 ONU online,
ONU mati, feeder putus, seluruh tabel optik kosong, redaman, suspend billing,
uplink putus dan ONU tanpa MAC eksplisit. `scripts/HsgqInterop.java` juga dapat
dikompilasi dan dijalankan pada jaringan deployment; community diberikan lewat
environment `FTTH_SNMP_COMMUNITY`, bukan argumen command line.
