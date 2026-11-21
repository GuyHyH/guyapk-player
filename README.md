# GuyAPK

โปรแกรมเล่นแอป Android บน Windows และ macOS เลือกหน้าจอมือถือ แท็บเล็ต หรือจอพับได้ ทั้งแนวตั้งและแนวนอน ใช้งานได้ทั้งภาษาไทยและภาษาอังกฤษ

## ติดตั้ง

| ระบบ | ไฟล์ | หมายเหตุ |
|---|---|---|
| Windows 10/11 (64 บิต) | `dist/GuyAPK-1.0.0-Windows-x64-Setup.exe` | ไม่ต้องใช้สิทธิ์ผู้ดูแลระบบ |
| macOS 11 ขึ้นไป | `GuyAPK-1.0.0-macOS-AppleSilicon.dmg` หรือ `-Intel.dmg` | ต้อง build บน Mac ตามหัวข้อด้านล่าง |

ถ้าเครื่องมี Android SDK อยู่แล้ว GuyAPK จะใช้ของเดิมทันที ถ้ายังไม่มี โปรแกรมจะให้อ่านและยอมรับสัญญาอนุญาต แล้วดาวน์โหลดจาก Google ประมาณ 2 GB และควรมีพื้นที่ว่างอย่างน้อย 6 GB

Windows ต้องเปิด **Windows Hypervisor Platform** และเปิด Virtualization ใน BIOS

## วิธีใช้

1. เลือกอุปกรณ์และทิศทางหน้าจอ
2. ลากไฟล์ `.apk`, `.xapk`, `.apks` หรือ `.apkm` มาวาง
3. กด **ติดตั้งและเล่น** แอปจะแสดงในหน้าต่าง Android แยกต่างหากเพื่อให้ได้ความเร็วสูงสุด

เปลี่ยนแนวตั้งหรือแนวนอนได้ทันทีระหว่างเล่น ส่วนการเปลี่ยนขนาดจอหรือโหมดประสิทธิภาพต้องเริ่มเครื่องใหม่ แอปและข้อมูลจะยังอยู่ครบ

## ข้อจำกัด

- แอปที่ตรวจจับเครื่องจำลองหรือใช้ Play Integrity เช่น แอปธนาคารและเกมบางเกม อาจไม่ยอมทำงาน
- แอปที่มีเฉพาะ ARM บนเครื่อง Intel/AMD จะทำงานผ่านตัวแปลงคำสั่ง จึงอาจช้าลงหรือใช้ไม่ได้
- Mac ชิป Apple รัน image แบบ ARM จึงไม่รองรับแอปที่มีเฉพาะ x86

## Build จากซอร์สโค้ด

ต้องใช้ Go 1.26 ขึ้นไป

```powershell
./scripts/build-windows.ps1 -Version 1.0.0
```

บน Mac ต้องมี Xcode Command Line Tools:

```bash
./scripts/build-mac.sh 1.0.0 all
```

หากจะแจกจ่ายบน Mac ให้ตั้ง `MAC_SIGN_ID` เป็น Developer ID และทำ Notarization มิฉะนั้น macOS จะเตือนตอนเปิดครั้งแรก

ไฟล์ `.github/workflows/build.yml` ใช้สร้างตัวติดตั้งทั้งสองระบบบน GitHub Actions

## ทดสอบ

```powershell
go test ./internal/...
```

ทดสอบกับเครื่องจำลองจริง:

```powershell
$env:GUYAPK_LIVE_APK="C:\path\app.apk"; go test ./internal/android -run TestLive -v -timeout 20m
```

## เครดิต

ได้แนวคิดการจัดการหน้าจอจาก ApkStart (MIT) สร้างด้วย Wails, androidbinary และ ulikunitz/xz ส่วน Android Emulator และ system image เป็นของ Google ภายใต้ Android SDK License
