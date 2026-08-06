@echo off
setlocal
rem Verificacao 1:1 pela linha de comando: compara um cadastro guardado em
rem arquivo com o dedo encostado agora.
rem
rem   conferir-biometria.cmd [arquivo]
rem
rem O arquivo aceita as duas formas em que um cadastro costuma sair do banco:
rem o template puro, ou uma linha CHAVE=valor (BIOMETRIA_LEO=AQAAAB...).
rem O padrao e .env.hash ao lado deste script.
rem
rem Dentro de uma sessao RDP a comparacao vai sozinha para o servico comparador,
rem se houver um anunciado - e o que faz isso funcionar la, onde comparar no
rem proprio processo derruba o programa.
rem
rem O template NUNCA e impresso: so tamanho e um sha256 curto. E dado pessoal
rem irrevogavel, e uma janela de terminal e um lugar por onde ele nao deve
rem passar.

cd /d "%~dp0"

set "ARQ=%~1"
if "%ARQ%"=="" set "ARQ=%~dp0.env.hash"

set "EXE=%~dp0AgenteBiometria.exe"
if not exist "%EXE%" set "EXE=%ProgramFiles(x86)%\AgenteBiometria\AgenteBiometria.exe"
if not exist "%EXE%" (
    echo Nao achei o AgenteBiometria.exe nem aqui nem em Program Files ^(x86^).
    exit /b 1
)

if not exist "%ARQ%" (
    echo Nao achei o arquivo com o cadastro: %ARQ%
    echo.
    echo Use: conferir-biometria.cmd caminho\do\cadastro.hash
    exit /b 1
)

"%EXE%" --conferir-contra "%ARQ%"
set RC=%ERRORLEVEL%

echo.
echo ----------------------------------------------------------
if "%RC%"=="0" echo   RESULTADO: CONFERE - e a mesma pessoa.
if "%RC%"=="2" echo   RESULTADO: NAO CONFERE - dedo diferente ou leitura ruim.
if "%RC%"=="1" echo   RESULTADO: FALHOU - veja o erro acima. Nao e veredito
if "%RC%"=="1" echo              biometrico: nada foi comparado.
echo ----------------------------------------------------------
echo   codigo de saida: %RC%   ^(0 confere, 1 falhou, 2 nao confere^)
echo ----------------------------------------------------------

exit /b %RC%
