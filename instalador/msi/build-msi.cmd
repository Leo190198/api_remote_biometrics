@echo off
setlocal
rem Gera o MSI de instalacao por maquina.
rem
rem   build-msi.cmd [versao]
rem
rem Compila o agente para windows/386, embute o icone e empacota. O WiX vem do
rem cache NuGet - nao precisa estar instalado na maquina.

cd /d "%~dp0"
set "RAIZ=%~dp0..\.."
set "VERSAO=%~1"
if "%VERSAO%"=="" set "VERSAO=1.2.0"

rem Ambiente de build ja usado neste servidor para outros MSI.
if "%DOTNET_ROOT%"=="" set "DOTNET_ROOT=D:\dotnet-sdk"
if "%NUGET_PACKAGES%"=="" set "NUGET_PACKAGES=D:\nuget-cache"
set "PATH=%DOTNET_ROOT%;%PATH%"

where dotnet >nul 2>&1
if errorlevel 1 (
    echo Nao achei o dotnet. Ajuste DOTNET_ROOT e tente de novo.
    exit /b 1
)

rem --------------------------------------------------------------- 1) binario
echo [1/3] compilando o agente (windows/386)...
pushd "%RAIZ%"
for /f %%c in ('git rev-parse --short HEAD 2^>nul') do set "COMMIT=%%c"
if "%COMMIT%"=="" set "COMMIT=local"
set GOOS=windows
set GOARCH=386
set CGO_ENABLED=0
set GOTOOLCHAIN=auto
go build -ldflags "-s -w -H windowsgui -X main.versao=%VERSAO% -X main.commit=%COMMIT%" -o AgenteBiometria.exe .
if errorlevel 1 (
    popd
    echo Falhou o go build.
    exit /b 1
)

rem O go build nao gera recurso PE, entao o icone da aplicacao entra depois.
rem Sem este passo o exe sai sem icone e ninguem percebe ate ver no Explorer.
echo [2/3] embutindo o icone...
python embutir-icone.py AgenteBiometria.exe app.ico
if errorlevel 1 (
    popd
    echo Falhou ao embutir o icone.
    exit /b 1
)
popd

rem ------------------------------------------------------------------ 2) MSI
echo [3/3] empacotando o MSI...
dotnet build AgenteBiometria.wixproj -c Release ^
    -p:Versao=%VERSAO% -p:PastaFontes="%RAIZ%" ^
    -v:minimal
if errorlevel 1 (
    echo Falhou o empacotamento.
    exit /b 1
)

echo.
for /f "delims=" %%f in ('dir /b /s "bin\Release\*.msi" 2^>nul') do echo MSI: %%f
echo.
echo Instalar:    msiexec /i AgenteBiometria.msi /qn /l*v instalacao.log
echo Desinstalar: msiexec /x AgenteBiometria.msi /qn
endlocal
