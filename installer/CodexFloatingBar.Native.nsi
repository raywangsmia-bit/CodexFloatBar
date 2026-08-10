Unicode true
RequestExecutionLevel user

!include "MUI2.nsh"

!ifndef SOURCE_DIR
  !error "SOURCE_DIR must point to the portable application directory"
!endif
!ifndef OUTPUT_FILE
  !error "OUTPUT_FILE must be the installer output path"
!endif
!ifndef APP_ID
  !error "APP_ID must be supplied from release metadata"
!endif
!ifndef APP_DISPLAY_NAME
  !error "APP_DISPLAY_NAME must be supplied from release metadata"
!endif
!ifndef APP_VERSION
  !error "APP_VERSION must be supplied from release metadata"
!endif
!ifndef APP_CHANNEL
  !error "APP_CHANNEL must be supplied from release metadata"
!endif
!ifndef APP_PUBLISHER
  !error "APP_PUBLISHER must be supplied from release metadata"
!endif
!ifndef APP_WEBSITE
  !error "APP_WEBSITE must be supplied from release metadata"
!endif
!ifndef EXECUTABLE_NAME
  !error "EXECUTABLE_NAME must be supplied from release metadata"
!endif
!ifndef INSTALL_DIR_NAME
  !error "INSTALL_DIR_NAME must be supplied from release metadata"
!endif
!ifndef START_MENU_FOLDER
  !error "START_MENU_FOLDER must be supplied from release metadata"
!endif
!ifndef STARTUP_VALUE_NAME
  !error "STARTUP_VALUE_NAME must be supplied from release metadata"
!endif
!ifndef UNINSTALL_KEY
  !error "UNINSTALL_KEY must be supplied from release metadata"
!endif
!ifndef WINDOW_CLASS
  !error "WINDOW_CLASS must be supplied from release metadata"
!endif
!ifndef WINDOW_TITLE
  !error "WINDOW_TITLE must be supplied from release metadata"
!endif

!define APP_REGISTRY_KEY "Software\${APP_ID}"
!define RUN_REGISTRY_KEY "Software\Microsoft\Windows\CurrentVersion\Run"
!define UNINSTALL_REGISTRY_KEY \
  "Software\Microsoft\Windows\CurrentVersion\Uninstall\${UNINSTALL_KEY}"

Name "${APP_DISPLAY_NAME}"
BrandingText "${APP_DISPLAY_NAME} ${APP_VERSION}"
OutFile "${OUTPUT_FILE}"
InstallDir "$LOCALAPPDATA\Programs\${INSTALL_DIR_NAME}"
SetCompressor /SOLID lzma
SetOverwrite on

Var PreserveStartup

!insertmacro MUI_PAGE_WELCOME
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_INSTFILES
!insertmacro MUI_PAGE_FINISH
!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES
!insertmacro MUI_LANGUAGE "SimpChinese"

Function LoadPreviousInstallState
  StrCpy $PreserveStartup "0"

  SetRegView 64
  ReadRegStr $0 HKCU "${APP_REGISTRY_KEY}" "InstallDir"
  StrCmp $0 "" read_install_dir_32 install_dir_found
read_install_dir_32:
  SetRegView 32
  ReadRegStr $0 HKCU "${APP_REGISTRY_KEY}" "InstallDir"
  SetRegView 64
  StrCmp $0 "" read_startup_value install_dir_found
install_dir_found:
  StrCpy $INSTDIR $0

read_startup_value:
  SetRegView 64
  ReadRegStr $1 HKCU "${RUN_REGISTRY_KEY}" "${STARTUP_VALUE_NAME}"
  StrCmp $1 "" read_startup_value_32 startup_value_found
read_startup_value_32:
  SetRegView 32
  ReadRegStr $1 HKCU "${RUN_REGISTRY_KEY}" "${STARTUP_VALUE_NAME}"
  SetRegView 64
  StrCmp $1 "" previous_state_done startup_value_found
startup_value_found:
  StrCpy $PreserveStartup "1"
previous_state_done:
  SetRegView 64
FunctionEnd

Function EnsureApplicationClosed
check_application_again:
  FindWindow $0 "${WINDOW_CLASS}" "${WINDOW_TITLE}"
  StrCmp $0 0 application_closed
  IfSilent application_running_silent application_running_interactive

application_running_interactive:
  MessageBox MB_RETRYCANCEL|MB_ICONEXCLAMATION "${APP_DISPLAY_NAME} is still running. Exit it from the tray menu, then select Retry. Setup will not terminate the process." IDRETRY check_application_again IDCANCEL application_close_canceled
application_close_canceled:
  Abort

application_running_silent:
  SetErrorLevel 32
  Abort

application_closed:
FunctionEnd

Function .onInit
  SetShellVarContext current
  Call LoadPreviousInstallState
  Call EnsureApplicationClosed
FunctionEnd

Function un.EnsureApplicationClosed
un_check_application_again:
  FindWindow $0 "${WINDOW_CLASS}" "${WINDOW_TITLE}"
  StrCmp $0 0 un_application_closed
  IfSilent un_application_running_silent un_application_running_interactive

un_application_running_interactive:
  MessageBox MB_RETRYCANCEL|MB_ICONEXCLAMATION "${APP_DISPLAY_NAME} is still running. Exit it from the tray menu, then select Retry. Uninstall will not terminate the process." IDRETRY un_check_application_again IDCANCEL un_application_close_canceled
un_application_close_canceled:
  Abort

un_application_running_silent:
  SetErrorLevel 32
  Abort

un_application_closed:
FunctionEnd

Function un.onInit
  SetShellVarContext current
  SetRegView 64
  Call un.EnsureApplicationClosed
FunctionEnd

Section "${APP_DISPLAY_NAME}" MainSection
  ; Remove legacy 32-bit metadata before writing the canonical 64-bit entry.
  SetRegView 32
  DeleteRegKey HKCU "${APP_REGISTRY_KEY}"
  DeleteRegKey HKCU "${UNINSTALL_REGISTRY_KEY}"
  SetRegView 64

  ; The running-process gate above makes an in-place upgrade safe. Removing only
  ; the generated UI directory prevents stale surface generations from surviving.
  RMDir /r "$INSTDIR\ui"
  SetOutPath "$INSTDIR"
  File "${SOURCE_DIR}\${EXECUTABLE_NAME}"
  File "${SOURCE_DIR}\release.json"
  File "${SOURCE_DIR}\LICENSE"
  File "${SOURCE_DIR}\THIRD_PARTY_NOTICES.txt"
  SetOutPath "$INSTDIR\ui\dist"
  File /r "${SOURCE_DIR}\ui\dist\*"

  WriteUninstaller "$INSTDIR\Uninstall.exe"
  WriteRegStr HKCU "${APP_REGISTRY_KEY}" "InstallDir" "$INSTDIR"
  WriteRegStr HKCU "${APP_REGISTRY_KEY}" "Version" "${APP_VERSION}"
  WriteRegStr HKCU "${APP_REGISTRY_KEY}" "Channel" "${APP_CHANNEL}"

  CreateDirectory "$SMPROGRAMS\${START_MENU_FOLDER}"
  CreateShortcut \
    "$SMPROGRAMS\${START_MENU_FOLDER}\${APP_DISPLAY_NAME}.lnk" \
    "$INSTDIR\${EXECUTABLE_NAME}"
  CreateShortcut \
    "$SMPROGRAMS\${START_MENU_FOLDER}\Uninstall.lnk" \
    "$INSTDIR\Uninstall.exe"

  WriteRegStr HKCU "${UNINSTALL_REGISTRY_KEY}" \
    "DisplayName" "${APP_DISPLAY_NAME}"
  WriteRegStr HKCU "${UNINSTALL_REGISTRY_KEY}" \
    "DisplayVersion" "${APP_VERSION}"
  WriteRegStr HKCU "${UNINSTALL_REGISTRY_KEY}" \
    "Publisher" "${APP_PUBLISHER}"
  WriteRegStr HKCU "${UNINSTALL_REGISTRY_KEY}" \
    "InstallLocation" "$INSTDIR"
  WriteRegStr HKCU "${UNINSTALL_REGISTRY_KEY}" \
    "URLInfoAbout" "${APP_WEBSITE}"
  WriteRegStr HKCU "${UNINSTALL_REGISTRY_KEY}" \
    "UninstallString" "$\"$INSTDIR\Uninstall.exe$\""
  WriteRegStr HKCU "${UNINSTALL_REGISTRY_KEY}" \
    "QuietUninstallString" \
    "$\"$INSTDIR\${EXECUTABLE_NAME}$\" --quiet-uninstall"
  WriteRegDWORD HKCU "${UNINSTALL_REGISTRY_KEY}" "NoModify" 1
  WriteRegDWORD HKCU "${UNINSTALL_REGISTRY_KEY}" "NoRepair" 1
  SectionGetSize ${MainSection} $0
  WriteRegDWORD HKCU "${UNINSTALL_REGISTRY_KEY}" "EstimatedSize" $0

  StrCmp $PreserveStartup "1" 0 startup_preservation_done
  WriteRegStr HKCU "${RUN_REGISTRY_KEY}" \
    "${STARTUP_VALUE_NAME}" "$\"$INSTDIR\${EXECUTABLE_NAME}$\""
startup_preservation_done:
SectionEnd

Section "Uninstall"
  Delete "$SMPROGRAMS\${START_MENU_FOLDER}\${APP_DISPLAY_NAME}.lnk"
  Delete "$SMPROGRAMS\${START_MENU_FOLDER}\Uninstall.lnk"
  RMDir "$SMPROGRAMS\${START_MENU_FOLDER}"

  ; Antivirus scanners can briefly retain the freshly launched executable after
  ; the process exits. Retry the exact file before falling back to moving it out
  ; of the installation directory and scheduling deletion on reboot.
  StrCpy $0 0
un_delete_executable:
  Delete "$INSTDIR\${EXECUTABLE_NAME}"
  IfFileExists \
    "$INSTDIR\${EXECUTABLE_NAME}" \
    un_executable_locked \
    un_executable_deleted
un_executable_locked:
  IntOp $0 $0 + 1
  IntCmp $0 50 un_move_executable un_retry_executable un_move_executable
un_retry_executable:
  Sleep 100
  Goto un_delete_executable
un_move_executable:
  GetTempFileName $1
  Delete $1
  Rename "$INSTDIR\${EXECUTABLE_NAME}" $1
  IfFileExists \
    "$INSTDIR\${EXECUTABLE_NAME}" \
    un_schedule_original_executable \
    un_schedule_moved_executable
un_schedule_moved_executable:
  Delete /REBOOTOK $1
  Goto un_executable_deleted
un_schedule_original_executable:
  Delete /REBOOTOK "$INSTDIR\${EXECUTABLE_NAME}"
un_executable_deleted:
  Delete "$INSTDIR\release.json"
  Delete "$INSTDIR\LICENSE"
  Delete "$INSTDIR\THIRD_PARTY_NOTICES.txt"
  Delete "$INSTDIR\Uninstall.exe"
  RMDir /r "$INSTDIR\ui"
  RMDir "$INSTDIR"

  ; Remove only this native Next Beta startup value. The WPF value is named
  ; CodexFloatingBar and is intentionally untouched.
  SetRegView 64
  DeleteRegValue HKCU "${RUN_REGISTRY_KEY}" "${STARTUP_VALUE_NAME}"
  DeleteRegKey HKCU "${APP_REGISTRY_KEY}"
  DeleteRegKey HKCU "${UNINSTALL_REGISTRY_KEY}"
  SetRegView 32
  DeleteRegValue HKCU "${RUN_REGISTRY_KEY}" "${STARTUP_VALUE_NAME}"
  DeleteRegKey HKCU "${APP_REGISTRY_KEY}"
  DeleteRegKey HKCU "${UNINSTALL_REGISTRY_KEY}"
  SetRegView 64
SectionEnd
