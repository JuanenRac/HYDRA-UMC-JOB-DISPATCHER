<p align="center">
  <img src="images/HYDRA_UMC_BANNER.svg" alt="HYDRA-UMC-JOB-DISPATCHER banner" width="100%">
</p>

# 📋 HYDRA-UMC-JOB-DISPATCHER

<p align="center"><a href="README.md">🇺🇸 English</a> | <a href="README_spa.md">🇪🇸 Español</a> | <a href="README_fra.md">🇫🇷 Français</a> | <a href="README_ita.md">🇮🇹 Italiano</a> | 🇩🇪 <b>Deutsch</b> | <a href="README_zho.md">🇨🇳 简体中文</a> | <a href="README_jpn.md">🇯🇵 日本語</a></p>

### ⚙️ Prioritätsbasierte Missions-Warteschlange für heterogene Roboterflotten

<p align="left">
  <img src="https://img.shields.io/badge/Lizenz-GPL%203.0-blue.svg" alt="GPL 3.0">
  <img src="https://img.shields.io/badge/Architektur-Event--Driven-blue.svg" alt="Event-Driven">
  <img src="https://img.shields.io/badge/Technologie-Go%20%2F%20net%2Fhttp-00ADD8.svg" alt="Tech">
</p>

---

## 1. 🛠️ TECHNISCHER ÜBERBLICK

**HYDRA-UMC-JOB-DISPATCHER** ist die Engine zur Aufgabenverteilung des Orchestrators. Sie verwaltet eine globale Missions-Warteschlange und verteilt Aufgaben an einzelne Roboter basierend auf deren aktueller Verfügbarkeit, Standort und angebautem Werkzeug (URTC).

Er stellt sicher, dass hochpriore Aufgaben (z. B. "Notfall-Defektbehebung") den normalen Produktionsfluss umgehen, und koordiniert mehrstufige Montage-Sequenzen, die die Zusammenarbeit verschiedener Roboter erfordern.

### Hauptmerkmale:
* 📋 **Dynamisches Queueing:** Intelligente Missionspriorisierung und -planung.
* ⚖️ **Werkzeugbewusstes Routing:** Leitet Aufgaben automatisch an den Roboter mit dem richtigen URTC-Kopf weiter.
* 🔄 **Mehrstufige Missionen:** Verwaltet Abhängigkeiten zwischen Aufgaben (z. B. muss "Pick" vor "Place" erfolgen).
* 📡 **Persistenz:** Fehlertoleranter Missionsstatus durch lokale Redis-/Datenbank-Speicherung.
* 🔁 **Idempotente Einreichung (v0):** Echte, optionale `DedupKey`-basierte Deduplizierung über `POST /jobs/submit` - eine erneute Einreichung eines laufenden oder bereits abgeschlossenen Jobs wird unverändert zurückgegeben, und ein Wiederholungsversuch nach einem echten Fehler nutzt dieselbe Job-ID weiter, statt die Arbeit zweimal auszuführen. Die Prioritätsreihenfolge ist über wiederholte Läufe hinweg deterministisch, abgedeckt durch einen expliziten Regressionstest.

---

## 2. 🔄 DISPATCHER-ABLAUF

```mermaid
flowchart LR
    REQ["Neue Missionsanfrage"] --> Q["Globale Missions-Warteschlange"]
    Q --> SCHED["Dispatcher-Planer"]
    DB[("Werkzeug- & Roboter-Register")] --> SCHED
    SCHED --> R1["Robot A (PnP)"]
    SCHED --> R2["Robot B (Laser)"]
    R1 -- Erfolg/Fehler --> SCHED
    SCHED --> LOG["Produktionsberichte"]
```

---

## 3. 🧱 ARCHITEKTUR & DESIGNENTSCHEIDUNGEN

* **Warum die echte Logik unter `src/` liegt, nicht im Repo-Root.** `src/dispatcher` (die Planungs-Engine) und `src/api` (die HTTP-Handler) enthalten die eigentliche Implementierung; `main.go`/`version.go` bleiben im Repo-Root als der Einstiegspunkt, der sie verbindet.
* **Warum die Auftragszuweisung die Werkzeugverfügbarkeit über URTC prüft.** Ein Auftrag, der einen bestimmten Werkzeugkopf benötigt, ist nur einem Roboter zuweisbar, dessen URTC-gesteuerter Werkzeugkopf tatsächlich vorhanden und untätig ist - dies vor der Zuweisung zu prüfen (nicht nach einem gescheiterten Griff) verhindert, dass ein Roboter an einer Station ankommt, die er tatsächlich nicht nutzen kann. `src/dispatcher.Engine.DispatchOnce` setzt das heute bereits per exaktem Abgleich von `RequiredTool` und `Robot.Tool` durch; es spricht noch nicht mit einem echten URTC über CAN, um zu bestätigen, dass der Werkzeugkopf physisch angebracht ist (die Engine kennt nur, was die Roboter-Registrierung über `POST /robots` behauptet).
* **Warum der Scheduler heute schon echt ist, die Persistenz aber nicht.** `src/dispatcher` implementiert den echten Algorithmus, den das "DISPATCHER FLOW"-Diagramm des READMEs beschreibt: eine global nach Priorität sortierte Warteschlange, werkzeugbewusstes Routing und mehrstufige Abhängigkeiten (ein Auftrag bleibt `blocked`, bis jeder Auftrag in seinem `DependsOn` `done` erreicht). All das lebt nur im Speicher - der Zustand von `Engine` bleibt hinter exportierten Methoden, genau damit ein Redis-/DB-gestützter Speicher später das ersetzen kann, was hinter diesen Methoden steckt, ohne jeden Aufrufer zu ändern. Zu beweisen, dass der Scheduling-Algorithmus selbst korrekt ist, kam zuerst.
* **Warum die HTTP-API einfaches JSON/HTTP ist, kein gRPC.** Dies ist eine mensch-/betriebsorientierte Kontrollfläche (einen Auftrag einreichen, einen Roboter registrieren, nachfragen was passiert ist) - `hydra.common.v1` (der geteilte gRPC-Vertrag des Ökosystems, siehe `HYDRA-UMC-ORCHESTRATOR/proto/`) bleibt für Knoten-zu-Knoten-Verkehr reserviert, gemäß dem bereits dokumentierten Geltungsbereich dieses Protos.
* **Wie sich das ins restliche Ökosystem einfügt.** Ein Geschwisterdienst unter HYDRA-UMC-ORCHESTRATOR - verwandelt Entscheidungen auf Missionsebene in konkrete Auftragszuweisungen pro Roboter, geprüft gegen die Werkzeugverfügbarkeit von URTC und die eigenen Routen von HYDRA-UMC-PATH-PLANNER-3D.
* **Warum `POST /jobs/submit` eine neue Route ist, statt `POST /jobs` zu ändern.** `AddJob()`/`POST /jobs` fügen immer ein und schlagen bei einer ID-Kollision fehl - dieser Low-Level-Vertrag bleibt unangetastet. `SubmitJob()`/`POST /jobs/submit` legt darüber echte, optionale Deduplizierung via `Job.DedupKey` - dasselbe Muster, das im gesamten Ökosystem verwendet wird (ein abgesicherter Einstiegspunkt neben einer unveränderten Low-Level-Primitive, keine Verhaltensänderung, die ihr aufgepfropft wird).
* **Warum ein Wiederholungsversuch nach einem Fehler dieselbe Job-ID wiederverwendet, statt eine neue anzulegen.** Die Alternative - bei jedem Versuch einen neuen Job anzulegen - würde die Historie einer einzigen logischen Arbeitseinheit über mehrere IDs verstreuen und einem Aufrufer keine Möglichkeit geben, "das ist einmal fehlgeschlagen und wird wiederholt" von "das ist neue, unabhängige Arbeit" zu unterscheiden. Den ursprünglichen Job auf `Pending` zurückzusetzen hält seinen gesamten Lebenszyklus (einschließlich des fehlgeschlagenen Versuchs) unter einer einzigen ID.

---

## 📂 VERZEICHNISSTRUKTUR

```text
HYDRA-UMC-JOB-DISPATCHER/
├── src/
│   ├── dispatcher/    # Die echte Planungs-Engine: Warteschlange,
│   │                  # werkzeugbewusstes Routing, mehrstufige Abhängigkeiten
│   └── api/           # Einfache JSON/HTTP-Handler, die die Engine umschließen
├── docs/
│   └── API.md         # Echte HTTP-Endpunktreferenz (Requests, Responses, Statuscodes)
├── images/            # Medien und Diagramme
├── systemd/
│   └── hydra-umc-job-dispatcher.service # systemd-Unit der lokalen Prioritäts-Missionswarteschlange auf der CM5
├── tools/
│   ├── build_test.py  # Build-/Kompilierprüfung ohne Versionserhöhung
│   └── ci_validate.py # Manifest-/CHANGELOG-/Doku-Validierung, von der CI genutzt
├── build/             # Kompilierte Binärdateien (Ausgabe von build.sh/.bat)
├── go.mod / go.sum    # Go-Modul-Definition
├── version.go         # const Version = "X.Y.Z" (go.mod hat kein solches Feld)
├── main.go            # Einstiegspunkt: verbindet die Engine mit der HTTP-API und lauscht
├── bump_version.py    # Versions-Bump nach Kilometerzähler-Prinzip
├── bump_manifest_version.py # Synchronisiert die Version von hydra-umc.project.json mit der nativen (--sync)
├── build.sh/.bat      # Erhöht die Version, dann `go build`
├── run.sh/.bat        # Führt die kompilierte Binärdatei aus
└── README.md
```

Aus der ursprünglichen Vorlage entfernt: `hardware/`, `firmware/` und
`os/` — dies ist ein reiner Softwaredienst (Go-Binärdatei) ohne eigene
Hardware oder Firmware und ohne zu pflegendes Betriebssystem-Image. Siehe
[`docs/API.md`](docs/API.md) für die vollständige HTTP-Endpunktreferenz.

---

## 🔧 BUILD UND AUSFÜHRUNG

Eine echte priorisierte Auftragswarteschlange mit HTTP-API, nicht nur ein
kompilierbares Skelett.

```bash
# Windows
build.bat
run.bat -addr :8090

# Linux / macOS
./build.sh
./run.sh -addr :8090
```

`build.sh`/`build.bat` erhöhen die Version in `version.go` (ökosystemweite
Kilometerzähler-Regel, siehe `bump_version.py` - `go.mod` hat kein
natives Versionsfeld für Anwendungsbinärdateien) und führen anschließend
`go build` aus. `run.sh`/`run.bat` führen die resultierende Binärdatei
direkt aus.

```bash
# Roboter registrieren, Auftrag einreichen, verteilen, dann als erledigt markieren
curl -X POST localhost:8090/robots -d '{"id":"robot-a","tool":"PnP","available":true}'
curl -X POST localhost:8090/jobs   -d '{"id":"job-1","priority":5,"requiredTool":"PnP"}'
curl -X POST localhost:8090/dispatch -d '{}'
curl -X POST localhost:8090/jobs/complete -d '{"id":"job-1","success":true}'
curl localhost:8090/jobs
curl localhost:8090/robots
```

```bash
# Idempotente Einreichung: eine wiederholte Anfrage mit demselben dedupKey
# führt denselben Job nie zweimal aus, selbst wenn der Client eine andere id verwendet.
curl -X POST localhost:8090/jobs/submit -d '{"id":"job-2","priority":5,"requiredTool":"PnP","dedupKey":"req-abc"}'
# -> {"ID":"job-2", ..., "result":"created"}
curl -X POST localhost:8090/jobs/submit -d '{"id":"job-2-retry","priority":5,"requiredTool":"PnP","dedupKey":"req-abc"}'
# -> {"ID":"job-2", ..., "result":"duplicate"} - derselbe Job, unverändert

# Nach einem echten Fehlschlag nutzt ein Wiederholungsversuch mit demselben dedupKey die ID von job-2 weiter:
curl -X POST localhost:8090/jobs/complete -d '{"id":"job-2","success":false}'
curl -X POST localhost:8090/jobs/submit -d '{"id":"job-2-retry-2","priority":5,"requiredTool":"PnP","dedupKey":"req-abc"}'
# -> {"ID":"job-2", "Status":"pending", ..., "result":"retried"}
```

```bash
go test ./...   # src/dispatcher (Scheduling-Algorithmus) +
                 # src/api (echte HTTP-Roundtrips via httptest)
```

---

## 🚀 FAHRPLAN
* **Phase 1:** Deterministische Schwarm-Synchronisation über TSN und Sub-ms-Jitter-Reduzierung.
* **Phase 2:** 3D-Pfadplanung mit dynamischer Hindernisvermeidung in Multi-Roboter-Zellen.
* **Phase 3:** Multi-Roboter-Job-Dispatching-Optimierung unter Berücksichtigung der Ressourcenverfügbarkeit in Echtzeit.
* **Phase 4:** KI-gestützte Schätzung der Jobdauer für eine bessere Planung und Koordination heterogener Roboterflotten.

---

## 🔗 Verwandte Projekte

Dieses Projekt ist Teil des HYDRA-UMC-Robotik-Ökosystems desselben Autors (JuanenRac / Electro Hobby 3D). Gut zu wissen, da eine Anfrage eigentlich eines dieser Projekte betreffen könnte statt dieses Repositorys.

**Übergeordnetes Projekt**
- **[HYDRA-UMC-ORCHESTRATOR](https://github.com/JuanenRac/HYDRA-UMC-ORCHESTRATOR)** — Integrationsknoten mit einem echten gRPC/Protobuf-Health-Report-Vertrag und einer Missions-Zustandsmaschine; das übergeordnete Projekt, dessen spezifischer Orchestrierungsdienst dieses Repository innerhalb seiner eigenen Schwarmkoordinationsschicht ist.

**Geschwisterprojekte** — die übrigen Orchestrierungsdienste der eigenen Schwarmkoordinationsschicht von HYDRA-UMC-ORCHESTRATOR
- **[HYDRA-UMC-SWARM-SYNC](https://github.com/JuanenRac/HYDRA-UMC-SWARM-SYNC)** — echte CRDT-LWW-Element-Map-Zustandssynchronisation, eigenschaftsgetestet auf Multi-Zellen-Konvergenz.
- **[HYDRA-UMC-PATH-PLANNER-3D](https://github.com/JuanenRac/HYDRA-UMC-PATH-PLANNER-3D)** — echter RRT-basierter 3D-Pfadplaner mit echter Hindernis-/Arbeitsraum-Kollisionsvalidierung.
- **[HYDRA-UMC-NODE-HEALING](https://github.com/JuanenRac/HYDRA-UMC-NODE-HEALING)** — echter gRPC-basierter Flotten-Health-Watchdog mit Retry/Backoff und Identitäts-Mismatch-Erkennung.

**Direkt verwandt**
- **[URTC](https://github.com/JuanenRac/URTC)** — Firmware für die physische Universal-Robot-Tool-Controller-Platine, 25+ Werkzeugprofile über CAN-Bus — weist Jobs basierend darauf zu, welcher der eigenen Werkzeugköpfe von URTC tatsächlich verfügbar ist.
- **[HYDRA-UMC-PRODUCTION-REPORTS](https://github.com/JuanenRac/HYDRA-UMC-PRODUCTION-REPORTS)** — echte OEE-/Verfügbarkeitsberechnung über den DATALAKE-Verlauf, mit reproduzierbarem CSV-Export — das vorgesehene Ziel für Missionsabschluss-Protokolle; dieser Dispatcher ist die vorgesehene reale Quelle seiner eigenen OEE-`production_event`-Daten, sobald Abschlüsse zum Schreiben verdrahtet sind (noch nicht implementiert, auf der eigenen Seite jenes Projekts nachverfolgt).

**Ebenfalls Teil des Ökosystems**

*Kern-Hardware & Plattform*
- **[HYDRA-UMC](https://github.com/JuanenRac/HYDRA-UMC)** — das physische Motherboard des Roboterarms: CM5-Host + Dual-Core-STM32H745, koordiniert bis zu 8 Werkzeugarme über CAN-OTA/SPI-OTA.
- **[HYDRA-UMC-OS](https://github.com/JuanenRac/HYDRA-UMC-OS)** — reproduzierbare Raspberry-Pi-OS-Produktschicht für den CM5: schreibgeschützter Agent, validierte Konfiguration/Profile, WiFi-Ersteinrichtung.
- **[HYDRA-UMC-SDK](https://github.com/JuanenRac/HYDRA-UMC-SDK)** — der gemeinsame JSON-Schema-Vertrag und die Sicherheitsschranke, gegen die jede Bridge ihre Befehle validiert.

*Kern-Backend & Clients*
- **[HYDRA-UMC-SERVER](https://github.com/JuanenRac/HYDRA-UMC-SERVER)** — das reale Headless-Backend (REST/WebSocket), mit dem jeder Steuerungsclient tatsächlich spricht.
- **[HYDRA-UMC-STUDIO](https://github.com/JuanenRac/HYDRA-UMC-STUDIO)** — Web-Steuerungs-Dashboard mit Echtzeit-3D-Visualisierung mehrerer Roboter.
- **[HYDRA-UMC-SUITE](https://github.com/JuanenRac/HYDRA-UMC-SUITE)** — Desktop-Schwarmleitstand (PySide6) für mehrere Server gleichzeitig, verpackt als eigenständige ausführbare Datei.
- **[HYDRA-UMC-ANDROID-CONTROL](https://github.com/JuanenRac/HYDRA-UMC-ANDROID-CONTROL)** — native Android-Steuerungs-App mit biometrischem Login und einer gekoppelten Wear-OS-Begleit-App.
- **[HYDRA-UMC-IOS-CONTROL](https://github.com/JuanenRac/HYDRA-UMC-IOS-CONTROL)** — iOS/iPadOS-Steuerungs-App (Flutter) mit Echtzeit-WebSocket-Synchronisierung.
- **[HYDRA-UMC-DSI](https://github.com/JuanenRac/HYDRA-UMC-DSI)** — native Touch-UI für das eingebaute 7"-DSI-Touchscreen, direkt auf dem CM5 eingebettet.
- **[HYDRA-UMC-EDITOR-URDF](https://github.com/JuanenRac/HYDRA-UMC-EDITOR-URDF)** — grafischer Desktop-URDF-Ersteller/-Editor, der fertige Modelle in STUDIOs eigenen Katalog überträgt.
- **[HYDRA-UMC-BRIDGE-AMR](https://github.com/JuanenRac/HYDRA-UMC-BRIDGE-AMR)** — Koordinationsschranke für AGV-/AMR-Flotten über einen echten VDA-5050-MQTT-Publisher.
- **[HYDRA-UMC-BRIDGE-CNC](https://github.com/JuanenRac/HYDRA-UMC-BRIDGE-CNC)** — High-Level-Koordinator für CNC-Zellen mit echtem GRBL-Status-/Steuerbyte-Zugriff.
- **[HYDRA-UMC-BRIDGE-DROIDS](https://github.com/JuanenRac/HYDRA-UMC-BRIDGE-DROIDS)** — Koordinationsschranke für laufende/humanoide Droiden, mit einem echten Boston-Dynamics-Spot-Befehlssender.
- **[HYDRA-UMC-BRIDGE-LASER](https://github.com/JuanenRac/HYDRA-UMC-BRIDGE-LASER)** — Sicherheitskoordinator für Laserzellen, liest 3 echte Schlüssel-/Gehäuse-/Verriegelungs-GPIO-Sicherungen.
- **[HYDRA-UMC-BRIDGE-OPENPNP](https://github.com/JuanenRac/HYDRA-UMC-BRIDGE-OPENPNP)** — sicherer High-Level-Koordinator für den Leiterplattenfluss von OpenPnP Pick-and-Place.
- **[HYDRA-UMC-BRIDGE-PRINTER3D](https://github.com/JuanenRac/HYDRA-UMC-BRIDGE-PRINTER3D)** — sichere Koordinationsschranke für Moonraker/Klipper-3D-Drucker, mit echten gesicherten Job-Befehlen.
- **[HYDRA-UMC-BRIDGE-ROS2](https://github.com/JuanenRac/HYDRA-UMC-BRIDGE-ROS2)** — Sicherheitskoordinator mit einem echten, träge importierten rclpy-ROS-2-Transport.
- **[HYDRA-UMC-BRIDGE-UAV](https://github.com/JuanenRac/HYDRA-UMC-BRIDGE-UAV)** — Koordinationsschranke für kameraausgestattete UAVs, mit einem echten MAVLink-Befehlssender.

*URTC-Werkzeugplattform*
- **[URTC-FLASHER](https://github.com/JuanenRac/URTC-FLASHER)** — Desktop-GUI-Flash-Tool für URTC-Platinen, CAN-OTA plus Full-Chip-SWD/JTAG.
- **[URTC-TESTER](https://github.com/JuanenRac/URTC-TESTER)** — Desktop-Live-CAN-Bus-Diagnosetool für URTC-Platinen, ein Panel pro Werkzeugprofil.
- **[URTC-WEB-STUDIO](https://github.com/JuanenRac/URTC-WEB-STUDIO)** — browserbasierte Alternative zu URTC-TESTER über die Web-Serial-API, ohne lokale Installation.

*Vision-KI-Knoten (Hailo-8)*
- **[HYDRA-UMC-VISION-NODE](https://github.com/JuanenRac/HYDRA-UMC-VISION-NODE)** — Integrationsknoten für die Hailo-8-Vision-Pipeline, mit einer echten stufenweisen Hardware-Bereitschaftsprüfung.
- **[HYDRA-UMC-DETECTION-HEF](https://github.com/JuanenRac/HYDRA-UMC-DETECTION-HEF)** — echte Registry für kompilierte Modelle mit Hailo-Architektur-/Prüfsummen-Safe-Load-Verifizierung.
- **[HYDRA-UMC-VISION-STREAMER](https://github.com/JuanenRac/HYDRA-UMC-VISION-STREAMER)** — echter GStreamer-Pipeline- + MediaMTX-Konfigurationsgenerator mit einer echten HailoRT-Integrationsschranke.
- **[HYDRA-UMC-VISUAL-SERVOING-API](https://github.com/JuanenRac/HYDRA-UMC-VISUAL-SERVOING-API)** — echtes Position-Based-Visual-Servoing-Korrekturgesetz, sicherheitsgesteuert nach vorgelagertem Zonenstatus.
- **[HYDRA-UMC-SAFETY-ZONES](https://github.com/JuanenRac/HYDRA-UMC-SAFETY-ZONES)** — echte Zonenverletzungsprüfung und E-STOP-Anforderung, mit erzwungener Kalibrierungsaktualität.

*Kognitiver KI-Knoten (Hailo-10)*
- **[HYDRA-UMC-COGNITIVE-NODE](https://github.com/JuanenRac/HYDRA-UMC-COGNITIVE-NODE)** — Integrationsknoten für die Hailo-10-Cognitive-Pipeline (LLM-/VLA-/Sprach-Orchestrierung).
- **[HYDRA-UMC-VLA-ENGINE](https://github.com/JuanenRac/HYDRA-UMC-VLA-ENGINE)** — echte Aktions-Token-Kodierung/-Dekodierung und Trajektoriengenerierung für ein Vision-Language-Action-Modell.
- **[HYDRA-UMC-VOICE-UI](https://github.com/JuanenRac/HYDRA-UMC-VOICE-UI)** — echtes Sprach-Frontend (VAD + Intent-Parser) mit einem begrenzten, bestätigungsgesicherten Watch-Relay.
- **[HYDRA-UMC-SEMANTIC-PLANNER](https://github.com/JuanenRac/HYDRA-UMC-SEMANTIC-PLANNER)** — echte regelbasierte Aufgabenzerlegung und semantische Fehlerbehebung über MCU-Fehlercodes.
- **[HYDRA-UMC-DOCS-QA](https://github.com/JuanenRac/HYDRA-UMC-DOCS-QA)** — echte, nur auf der Standardbibliothek basierende TF-IDF-Dokumentensuche über die eigenen Markdown-Dokumente dieses Ökosystems.

*Digitaler Zwilling & Simulation*
- **[HYDRA-UMC-TWIN](https://github.com/JuanenRac/HYDRA-UMC-TWIN)** — Integrationsknoten für die Digital-Twin-Engine, mit einem echten Versionskompatibilitäts-Sync-Vertrag.
- **[HYDRA-UMC-HIL-BRIDGE](https://github.com/JuanenRac/HYDRA-UMC-HIL-BRIDGE)** — echte Hardware-in-the-Loop-Sicherheitsverriegelung, die Befehle zwischen Simulation und echter Hardware routet.
- **[HYDRA-UMC-PHYSICS-REPLICA](https://github.com/JuanenRac/HYDRA-UMC-PHYSICS-REPLICA)** — echte Vorwärtskinematik und Gelenkgrenzenvalidierung über eine echte URDF-Teilmenge.
- **[HYDRA-UMC-SYNTHETIC-DATA-GEN](https://github.com/JuanenRac/HYDRA-UMC-SYNTHETIC-DATA-GEN)** — echter prozeduraler 2D-Szenengenerator mit YOLO/COCO-Annotationsexport.

*Daten & Analytik*
- **[HYDRA-UMC-DATALAKE](https://github.com/JuanenRac/HYDRA-UMC-DATALAKE)** — echter sqlite3-gestützter Zeitreihenspeicher mit einer echten Ingest-/Abfrage-HTTP-API.
- **[HYDRA-UMC-ANOMALY-DETECTOR](https://github.com/JuanenRac/HYDRA-UMC-ANOMALY-DETECTOR)** — echter FFT- + statistischer Basislinien-Anomaliedetektor mit Drift-Überwachung.
- **[HYDRA-UMC-TELEMETRY-COLLECTOR](https://github.com/JuanenRac/HYDRA-UMC-TELEMETRY-COLLECTOR)** — echte CAN/WebSocket-Ingestion-Pipeline in DATALAKE, mit Sequenz-Deduplizierung.

*Industrie-Gateway*
- **[HYDRA-UMC-GATEWAY-INDUSTRIAL](https://github.com/JuanenRac/HYDRA-UMC-GATEWAY-INDUSTRIAL)** — Integrationsknoten, der zu Industrieprotokollen weiterleitet, mit einer echten Befehls-Allowlist-/Backpressure-Schicht.
- **[HYDRA-UMC-OPCUA-SERVER](https://github.com/JuanenRac/HYDRA-UMC-OPCUA-SERVER)** — echter OPC-UA-Adressraum, verifiziert mit einer echten Binärprotokoll-Client-Session.
- **[HYDRA-UMC-MQTT-BROKER](https://github.com/JuanenRac/HYDRA-UMC-MQTT-BROKER)** — echter MQTT-Broker mit optionaler Pro-Client-Authentifizierung und Topic-ACLs.
- **[HYDRA-UMC-MTCONNECT-ADAPTER](https://github.com/JuanenRac/HYDRA-UMC-MTCONNECT-ADAPTER)** — echte MTConnect-`/probe`- und `/current`-XML-Endpunkte mit Degraded-Mode-Ausgabe.

*Ergänzende Tools & Ökosystembetrieb*
- **[HYDRA-UMC-DASHBOARD-AI](https://github.com/JuanenRac/HYDRA-UMC-DASHBOARD-AI)** — Smart-Summaries- und Anomaly-Highlighting-Panels über DATALAKE/ANOMALY-DETECTOR, mit einem ehrlichen statistischen Fallback.
- **[HYDRA-UMC-TOOL-CLI](https://github.com/JuanenRac/HYDRA-UMC-TOOL-CLI)** — Flotten-CLI mit einem echten, stabilen Exit-Code-Vertrag, ein echter Live-Client der eigenen API von HYDRA-UMC-SERVER.
- **[HYDRA-UMC-WATCH](https://github.com/JuanenRac/HYDRA-UMC-WATCH)** — WearOS-Begleit-App mit echten haptischen Alarmen und einem Sprach-Relay zum gekoppelten Telefon.
- **[URTC-SMART-RACK](https://github.com/JuanenRac/URTC-SMART-RACK)** — Firmware für ein Platinenmontagegestell mit echter Werkzeug-ID-Dekodierung und Smart-Idle-Vorheizlogik.
- **[URTC-VISION-TOOL](https://github.com/JuanenRac/URTC-VISION-TOOL)** — Firmware plus ein echter Python-Vision-Begleiter für einen Thermal-/RGB-Inspektionswerkzeugkopf.
- **[HYDRA-UMC-UPDATER](https://github.com/JuanenRac/HYDRA-UMC-UPDATER)** — administratives Desktop-Tool, das jedes Repository in diesem Ökosystem entdeckt, klont und aktualisiert.


---

## 📚 Dokumentation & Community

- **[CONTRIBUTING.md](CONTRIBUTING.md)** — Technologie-Stack und Coding-Richtlinien für einen Pull Request.
- **[CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md)** — die in dieser Community erwarteten Verhaltensstandards.
- **[SECURITY.md](SECURITY.md)** — wie man eine Schwachstelle meldet, und die echten Sicherheitsschwerpunkte dieses Projekts.
- **[SUPPORT.md](SUPPORT.md)** — wo man Fragen stellt und Fehler meldet.
- **[LICENSE.md](LICENSE.md)** — die eigene Lizenz dieses Projekts.

## 👤 AUTOR
**JuanenRac** (Electro Hobby 3D)
📧 electrohobby3d@gmail.com
📺 [youtube.com/@electrohobby3d](https://youtube.com/@electrohobby3d)

## 📜 LIZENZ
GPL-3.0 - Siehe LICENSE für Details.
