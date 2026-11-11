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
!if "${APP_ID}" S== "io.rocksolidlabs.conductor"
  ; The GUID electron-builder derives from dev.conductor.app (UUID v5 of the
  ; id in its namespace, NsisTarget.js), and the earlier id's two keys.
  !define CONDUCTOR_EARLIER_GUID "3648324d-47e0-5ae4-adc7-00ce07fef04a"
  !define CONDUCTOR_EARLIER_UNINSTALL_KEY "Software\Microsoft\Windows\CurrentVersion\Uninstall\${CONDUCTOR_EARLIER_GUID}"
  !define CONDUCTOR_EARLIER_INSTALL_KEY "Software\${CONDUCTOR_EARLIER_GUID}"
  ; The earlier app's two files in its folder: common.nsh's
  ; APP_EXECUTABLE_FILENAME and UNINSTALL_FILENAME for its product name,
  ; Conductor (the uninstaller with the backslash before it, 24 characters).
  !define CONDUCTOR_EARLIER_EXE "Conductor.exe"
  !define CONDUCTOR_EARLIER_UNINSTALLER "\Uninstall Conductor.exe"
  ; Each stop says what could not be done and what the person does next.
  !define CONDUCTOR_EARLIER_FAILED "The earlier Conductor could not be removed. Close it and run this installer again."
  !define CONDUCTOR_EARLIER_PARTLY "The earlier Conductor was not fully removed. Run this installer again."
  !define CONDUCTOR_EARLIER_FOR_EVERYONE "An earlier Conductor is installed for everyone on this computer. Run this installer as administrator to replace it."
  !define CONDUCTOR_EARLIER_BY_HAND "An earlier Conductor is installed that this installer cannot remove: its uninstaller was not found, or its entry is not one its installer wrote. Remove it from the installed apps, then run this installer again."

  ; conductorEarlierStop ends the install before anything of the new app is
  ; copied.
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

  ; conductorEarlierJudge sets $conductorEarlierForm to what the move does
  ; with ROOT's entry of the earlier id: none (no uninstall entry it can
  ; open), run (an entry in the earlier installer's form, its uninstaller
  ; and Conductor.exe in its folder) or stop (anything else: nothing is
  ; copied, run or deleted). An uninstaller that is not found is a stop: a
  ; file that is gone and a folder that cannot be looked into answer
  ; FileExists the same, so it is no proof that the earlier app is gone.
  ; The form is, whole, what registryAddInstallInfo writes for an install
  ; under ROOT (include/installer.nsh of the templates: UninstallString
  ; '"$INSTDIR\Uninstall Conductor.exe" <mode>', InstallLocation "$INSTDIR",
  ; MODE the mode word of ROOT's install). UninstallString is a quote, an
  ; absolute path (a drive that exists, a colon, a backslash) to the
  ; uninstaller, a quote, one space and MODE, and nothing else; it is
  ; rebuilt from its quoted part and compared byte for byte, so a second
  ; quote, text before the first or after MODE, or another mode word is out
  ; of form. InstallLocation is required and names that file's folder (in
  ; any case, with one trailing backslash or none): the earlier uninstaller
  ; reads its folder from the install key, so the key must name the folder
  ; judged here, $conductorEarlierDir. Each step runs only once the one
  ; before it holds, and every variable a pass reads is set in that pass
  ; before it is read.
  !macro conductorEarlierJudge ROOT MODE
    StrCpy $conductorEarlierForm none
    !insertmacro conductorEarlierFound ${ROOT} "${CONDUCTOR_EARLIER_UNINSTALL_KEY}"
    ${IfNot} ${Errors}
      StrCpy $conductorEarlierForm stop
      ReadRegStr $conductorEarlierText ${ROOT} "${CONDUCTOR_EARLIER_UNINSTALL_KEY}" UninstallString
      !insertmacro GetInQuotes $conductorEarlierUninstaller $conductorEarlierText
      ${If} $conductorEarlierText S== `"$conductorEarlierUninstaller" ${MODE}`
        StrCpy $conductorEarlierText $conductorEarlierUninstaller "" -24
        StrCpy $conductorEarlierProbe $conductorEarlierUninstaller 1
        StrCpy $conductorEarlierDrive $conductorEarlierUninstaller 3
        StrCpy $conductorEarlierDir $conductorEarlierUninstaller -24
        ${If} $conductorEarlierText S== "${CONDUCTOR_EARLIER_UNINSTALLER}"
        ${AndIf} $conductorEarlierDrive S== "$conductorEarlierProbe:\"
        ${AndIf} ${FileExists} "$conductorEarlierDrive*.*"
          ReadRegStr $conductorEarlierLocation ${ROOT} "${CONDUCTOR_EARLIER_INSTALL_KEY}" InstallLocation
          ${If} $conductorEarlierLocation == $conductorEarlierDir
          ${OrIf} $conductorEarlierLocation == "$conductorEarlierDir\"
            ${If} ${FileExists} "$conductorEarlierUninstaller"
            ${AndIf} ${FileExists} "$conductorEarlierDir\${CONDUCTOR_EARLIER_EXE}"
              StrCpy $conductorEarlierForm run
            ${EndIf}
          ${EndIf}
        ${EndIf}
      ${EndIf}
    ${EndIf}
  !macroend

  ; conductorReplaceEarlier acts on ROOT's entry of the earlier id as
  ; conductorEarlierJudge finds it. One it can run is removed by
  ; electron-builder's own upgrade protocol (installUtil.nsh's
  ; uninstallOldVersion, which runs only for an installer's own id): a copy
  ; of its uninstaller run in place against its folder (_?=, so ExecWait
  ; returns once the removal has ended), --updated for the move-aside path
  ; that restores every file at one it cannot move and keeps the user data.
  ; The move deletes nothing itself: the earlier uninstaller removes its
  ; files and its two keys.
  !macro conductorReplaceEarlier ROOT MODE
    !insertmacro conductorEarlierJudge ${ROOT} "${MODE}"
    ${If} $conductorEarlierForm == stop
      !insertmacro conductorEarlierStop "${CONDUCTOR_EARLIER_BY_HAND}"
    ${ElseIf} $conductorEarlierForm == run
      DetailPrint `Removing the earlier Conductor (${ROOT}).`
      !insertmacro copyFile "$conductorEarlierUninstaller" "$PLUGINSDIR\conductor-earlier-uninstaller-${ROOT}.exe"
      ; A non-zero exit is tried again a second later, five tries in all,
      ; as the template's loop makes.
      StrCpy $conductorEarlierTry 0
      ${Do}
        IntOp $conductorEarlierTry $conductorEarlierTry + 1
        ClearErrors
        ExecWait '"$PLUGINSDIR\conductor-earlier-uninstaller-${ROOT}.exe" /S /KEEP_APP_DATA --updated ${MODE} _?=$conductorEarlierDir' $conductorEarlierExit
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
      ${If} ${FileExists} "$conductorEarlierDir\${CONDUCTOR_EARLIER_EXE}"
      ${OrIf} ${FileExists} "$conductorEarlierUninstaller"
        !insertmacro conductorEarlierStop "${CONDUCTOR_EARLIER_PARTLY}"
      ${EndIf}
    ${EndIf}
    ; Exit 0 is not taken on trust: both keys are gone before anything of
    ; the new app is copied.
    ${If} $conductorEarlierForm != none
      !insertmacro conductorEarlierFound ${ROOT} "${CONDUCTOR_EARLIER_UNINSTALL_KEY}"
      ${IfNot} ${Errors}
        !insertmacro conductorEarlierStop "${CONDUCTOR_EARLIER_PARTLY}"
      ${EndIf}
      !insertmacro conductorEarlierFound ${ROOT} "${CONDUCTOR_EARLIER_INSTALL_KEY}"
      ${IfNot} ${Errors}
        !insertmacro conductorEarlierStop "${CONDUCTOR_EARLIER_PARTLY}"
      ${EndIf}
    ${EndIf}
  !macroend

  !macro customUnInstallCheck
    Var /GLOBAL conductorEarlierForm
    Var /GLOBAL conductorEarlierProbe
    Var /GLOBAL conductorEarlierText
    Var /GLOBAL conductorEarlierDrive
    Var /GLOBAL conductorEarlierUninstaller
    Var /GLOBAL conductorEarlierDir
    Var /GLOBAL conductorEarlierLocation
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
    ; The move is a person's act: an update the app started (--updated,
    ; the template's isUpdated) never makes it. A successor that updates
    ; itself was installed by a run that made it.
    ${If} ${isUpdated}
      DetailPrint `An update the app started: an earlier Conductor, if any, is left for the next run of this installer by hand.`
    ${Else}
      ; An install for everyone needs this installer elevated: it stops
      ; before anything of the new app is copied.
      !insertmacro conductorEarlierFound HKLM "${CONDUCTOR_EARLIER_UNINSTALL_KEY}"
      ${IfNot} ${Errors}
      ${AndIfNot} ${UAC_IsAdmin}
        !insertmacro conductorEarlierStop "${CONDUCTOR_EARLIER_FOR_EVERYONE}"
      ${EndIf}
      !insertmacro conductorReplaceEarlier HKCU "/currentuser"
      !insertmacro conductorReplaceEarlier HKLM "/allusers"
    ${EndIf}
  !macroend
!endif
