; Conductor's NSIS additions (electron-builder nsis.include).
; The inbound rule for the UDP port every WebRTC connection uses (the app's
; forwarder on Windows, in front of the server in WSL). netsh needs
; administrator rights: a per-user install without them leaves the rule to
; the app, which offers it from Settings (an elevated netsh, once).
!macro customInstall
  nsExec::ExecToLog 'netsh advfirewall firewall add rule name="Conductor WebRTC (UDP 7877)" dir=in action=allow protocol=UDP localport=7877'
  Pop $0
!macroend

!macro customUnInstall
  nsExec::ExecToLog 'netsh advfirewall firewall delete rule name="Conductor WebRTC (UDP 7877)"'
  Pop $0
!macroend

; The move: an installer of io.rocksolidlabs.conductor, the rc line's
; successor, replaces an install made under the shell's earlier app id,
; dev.conductor.app, which both install to %LOCALAPPDATA%\Programs\Conductor.
; Compiled in only for that id (APP_ID, which electron-builder defines for the
; include script): an open-source rc (dev.conductor.app) and a staging
; installer (io.rocksolidlabs.conductor.staging) hold none of it.
;
; customUnInstallCheck, never customInit (.onInit, before any page: the rc
; would be gone before the licence was accepted). electron-builder inserts it
; in the install section (installSection.nsh, through installUtil.nsh's
; handleUninstallResult), after the pages, after its own running-app check
; and before any file is copied, in place of its check of this id's earlier
; uninstall, which the macro makes first.
!if "${APP_ID}" == "io.rocksolidlabs.conductor"
  ; The GUID electron-builder derives from dev.conductor.app (UUID v5 of the
  ; id in its namespace, NsisTarget.js), and the earlier id's two keys.
  !define CONDUCTOR_EARLIER_GUID "3648324d-47e0-5ae4-adc7-00ce07fef04a"
  !define CONDUCTOR_EARLIER_UNINSTALL_KEY "Software\Microsoft\Windows\CurrentVersion\Uninstall\${CONDUCTOR_EARLIER_GUID}"
  !define CONDUCTOR_EARLIER_INSTALL_KEY "Software\${CONDUCTOR_EARLIER_GUID}"
  !define CONDUCTOR_EARLIER_FAILED "The earlier Conductor could not be removed; nothing was changed. Close it and run this installer again."
  !define CONDUCTOR_EARLIER_PARTLY "The earlier Conductor was not fully removed. Run this installer again."
  !define CONDUCTOR_EARLIER_FOR_EVERYONE "An earlier Conductor is installed for everyone on this computer. Run this installer as administrator to replace it."

  ; conductorEarlierStop ends the install before anything is copied.
  !macro conductorEarlierStop MESSAGE
    DetailPrint `${MESSAGE}`
    MessageBox MB_OK|MB_ICONSTOP `${MESSAGE}` /SD IDOK
    SetErrorLevel 2
    Abort `${MESSAGE}`
  !macroend

  ; conductorEarlierFound sets the error flag when ROOT holds no KEY.
  !macro conductorEarlierFound ROOT KEY
    ClearErrors
    EnumRegKey $conductorEarlierProbe ${ROOT} `${KEY}` 0
  !macroend

  ; conductorReplaceEarlier removes the earlier id's install under ROOT, if
  ; any, by electron-builder's own upgrade protocol (installUtil.nsh's
  ; uninstallOldVersion, which runs only for an installer's own id): a copy
  ; of its uninstaller run in place against its folder (_?=, so ExecWait
  ; returns once the removal has ended), --updated for the move-aside path
  ; that restores every file at one it cannot move and keeps the user data.
  !macro conductorReplaceEarlier ROOT MODE
    !insertmacro conductorEarlierFound ${ROOT} "${CONDUCTOR_EARLIER_UNINSTALL_KEY}"
    ${IfNot} ${Errors}
      ReadRegStr $conductorEarlierUninstaller ${ROOT} "${CONDUCTOR_EARLIER_UNINSTALL_KEY}" UninstallString
      !insertmacro GetInQuotes $conductorEarlierUninstaller "$conductorEarlierUninstaller"
      ReadRegStr $conductorEarlierDir ${ROOT} "${CONDUCTOR_EARLIER_INSTALL_KEY}" InstallLocation
      ${If} $conductorEarlierDir == ""
      ${AndIf} $conductorEarlierUninstaller != ""
        Push $conductorEarlierUninstaller
        Call GetFileParent
        Pop $conductorEarlierDir
      ${EndIf}
      ${If} $conductorEarlierUninstaller == ""
      ${OrIfNot} ${FileExists} "$conductorEarlierUninstaller"
        ; The uninstaller is missing: a stale entry, its folder gone, is
        ; removed; with the folder still there, nothing can remove it.
        ${If} $conductorEarlierDir == ""
        ${OrIfNot} ${FileExists} "$conductorEarlierDir\*.*"
          DetailPrint `Removing the stale entry of the earlier Conductor (${ROOT}).`
          DeleteRegKey ${ROOT} "${CONDUCTOR_EARLIER_UNINSTALL_KEY}"
          DeleteRegKey ${ROOT} "${CONDUCTOR_EARLIER_INSTALL_KEY}"
        ${Else}
          !insertmacro conductorEarlierStop "${CONDUCTOR_EARLIER_FAILED}"
        ${EndIf}
      ${Else}
        DetailPrint `Removing the earlier Conductor (${ROOT}).`
        !insertmacro copyFile "$conductorEarlierUninstaller" "$PLUGINSDIR\conductor-earlier-uninstaller.exe"
        ; A non-zero exit is tried again a second later, five tries in all,
        ; as the template's loop makes.
        StrCpy $conductorEarlierTry 0
        ${Do}
          IntOp $conductorEarlierTry $conductorEarlierTry + 1
          ClearErrors
          ExecWait '"$PLUGINSDIR\conductor-earlier-uninstaller.exe" /S /KEEP_APP_DATA --updated ${MODE} _?=$conductorEarlierDir' $conductorEarlierExit
          ${If} ${Errors}
            StrCpy $conductorEarlierExit "-1"
          ${EndIf}
          ${If} $conductorEarlierExit == 0
          ${OrIf} $conductorEarlierTry >= 5
            ${ExitDo}
          ${EndIf}
          Sleep 1000
        ${Loop}
        ${If} $conductorEarlierExit != 0
          DetailPrint `The earlier Conductor's uninstaller exited with $conductorEarlierExit.`
          !insertmacro conductorEarlierStop "${CONDUCTOR_EARLIER_FAILED}"
        ${EndIf}
        ; Exit 0 is not taken on trust: the entry and the files are gone
        ; before anything is copied.
        !insertmacro conductorEarlierFound ${ROOT} "${CONDUCTOR_EARLIER_UNINSTALL_KEY}"
        ${IfNot} ${Errors}
          !insertmacro conductorEarlierStop "${CONDUCTOR_EARLIER_PARTLY}"
        ${EndIf}
        !insertmacro conductorEarlierFound ${ROOT} "${CONDUCTOR_EARLIER_INSTALL_KEY}"
        ${IfNot} ${Errors}
          !insertmacro conductorEarlierStop "${CONDUCTOR_EARLIER_PARTLY}"
        ${EndIf}
        ${If} ${FileExists} "$conductorEarlierDir\Conductor.exe"
        ${OrIf} ${FileExists} "$conductorEarlierUninstaller"
          !insertmacro conductorEarlierStop "${CONDUCTOR_EARLIER_PARTLY}"
        ${EndIf}
      ${EndIf}
    ${EndIf}
  !macroend

  !macro customUnInstallCheck
    Var /GLOBAL conductorEarlierProbe
    Var /GLOBAL conductorEarlierUninstaller
    Var /GLOBAL conductorEarlierDir
    Var /GLOBAL conductorEarlierExit
    Var /GLOBAL conductorEarlierTry
    ; The template's check of this id's earlier uninstall, made first; a
    ; failure to launch it is said and the install goes on, as there.
    ${If} ${Errors}
      DetailPrint `Uninstall was not successful. Not able to launch uninstaller!`
    ${ElseIf} $R0 != 0
      MessageBox MB_OK|MB_ICONEXCLAMATION "$(uninstallFailed): $R0"
      DetailPrint `Uninstall was not successful. Uninstaller error code: $R0.`
      SetErrorLevel 2
      Quit
    ${EndIf}
    ; An install for everyone needs this installer elevated: it stops
    ; before anything is changed.
    !insertmacro conductorEarlierFound HKLM "${CONDUCTOR_EARLIER_UNINSTALL_KEY}"
    ${IfNot} ${Errors}
    ${AndIfNot} ${UAC_IsAdmin}
      !insertmacro conductorEarlierStop "${CONDUCTOR_EARLIER_FOR_EVERYONE}"
    ${EndIf}
    !insertmacro conductorReplaceEarlier HKCU "/currentuser"
    !insertmacro conductorReplaceEarlier HKLM "/allusers"
  !macroend
!endif
