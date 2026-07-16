# Injeta o icone (app.ico) no executavel ja compilado, via UpdateResource do
# Windows — substitui o antigo rsrc_windows_386.syso (nao precisa da ferramenta
# rsrc nem de recompilar quando so' o icone muda).
#
#   python embutir-icone.py AgenteBiometria.exe app.ico
#
# Rode SEMPRE depois do "go build" (o build gera o exe sem icone).
import ctypes
import struct
import sys

RT_ICON = 3
RT_GROUP_ICON = 14
LANG_NEUTRO = 0

k32 = ctypes.windll.kernel32

# tipos explicitos: sem eles, em Python 64-bit o HANDLE (ponteiro) seria
# truncado para 32 bits e o EndUpdateResource falharia/corromperia
k32.BeginUpdateResourceW.restype = ctypes.c_void_p
k32.BeginUpdateResourceW.argtypes = [ctypes.c_wchar_p, ctypes.c_int]
k32.UpdateResourceW.restype = ctypes.c_int
k32.UpdateResourceW.argtypes = [
    ctypes.c_void_p, ctypes.c_void_p, ctypes.c_void_p,
    ctypes.c_ushort, ctypes.c_char_p, ctypes.c_uint,
]
k32.EndUpdateResourceW.restype = ctypes.c_int
k32.EndUpdateResourceW.argtypes = [ctypes.c_void_p, ctypes.c_int]


def falha(msg):
    raise SystemExit(f"erro: {msg} (GetLastError={k32.GetLastError()})")


def main(exe, ico):
    dados = open(ico, "rb").read()
    reservado, tipo, qtd = struct.unpack_from("<HHH", dados, 0)
    if reservado != 0 or tipo != 1 or qtd == 0:
        raise SystemExit(f"{ico} nao parece um .ico valido")

    imagens = []  # (entrada de 12 bytes sem o offset, bytes da imagem)
    for i in range(qtd):
        ent = dados[6 + 16 * i : 6 + 16 * i + 16]
        tam, off = struct.unpack_from("<II", ent, 8)
        imagens.append((ent[:12], dados[off : off + tam]))

    # GRPICONDIR: mesmo cabecalho, mas cada entrada termina em nID (u16)
    grupo = struct.pack("<HHH", 0, 1, qtd)
    for i, (ent12, _) in enumerate(imagens):
        grupo += ent12 + struct.pack("<H", i + 1)

    h = k32.BeginUpdateResourceW(exe, False)
    if not h:
        falha(f"BeginUpdateResource em {exe} — ele esta aberto/rodando?")
    for i, (_, img) in enumerate(imagens):
        if not k32.UpdateResourceW(h, RT_ICON, i + 1, LANG_NEUTRO, img, len(img)):
            falha(f"UpdateResource RT_ICON {i + 1}")
    if not k32.UpdateResourceW(h, RT_GROUP_ICON, 1, LANG_NEUTRO, grupo, len(grupo)):
        falha("UpdateResource RT_GROUP_ICON")
    if not k32.EndUpdateResourceW(h, False):
        falha("EndUpdateResource")
    print(f"icone de {ico} ({qtd} tamanhos) embutido em {exe}")


if __name__ == "__main__":
    if len(sys.argv) != 3:
        raise SystemExit("uso: python embutir-icone.py <exe> <ico>")
    main(sys.argv[1], sys.argv[2])
