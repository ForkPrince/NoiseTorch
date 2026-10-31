CFLAGS = -I ../rnnoise/include -I ../rnnoise/src -Wall -Werror -Wno-error=cpp -O2 -fPIC

RNNOISE_SRC = ../rnnoise/src/denoise.c \
	../rnnoise/src/rnn.c \
	../rnnoise/src/pitch.c \
	../rnnoise/src/kiss_fft.c \
	../rnnoise/src/celt_lpc.c \
	../rnnoise/src/nnet.c \
	../rnnoise/src/nnet_default.c \
	../rnnoise/src/parse_lpcnet_weights.c \
	../rnnoise/src/rnnoise_data.c \
	../rnnoise/src/rnnoise_tables.c \
	../rnnoise/src/x86/x86_dnn_map.c \
	../rnnoise/src/x86/x86cpu.c

default:
	$(CC) $(CFLAGS) -c ../c-ringbuf/ringbuf.c $(RNNOISE_SRC) module.c
	$(CC) $(CFLAGS) -msse4.1 -c ../rnnoise/src/x86/nnet_sse4_1.c
	$(CC) $(CFLAGS) -mavx2 -c ../rnnoise/src/x86/nnet_avx2.c
	$(CC) -o rnnoise_ladspa.so *.o -shared -Wl,--version-script=export.txt -lm
