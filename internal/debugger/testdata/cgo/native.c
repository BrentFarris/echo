#include "native.h"
#include "_cgo_export.h"

typedef struct {
    int value;
    int *items;
    const char *label;
} Sample;

int native_global = 10;

int native_leaf(int value) {
    int doubled = value * 2;
    return doubled;
}

int native_add(int value) {
    int items[3] = {1, 2, 3};
    Sample sample = {value, items, "native"};
    Sample *ptr = &sample;
    int local = sample.value + native_global;
    local = native_leaf(local);
    return local + ptr->items[1];
}

int native_callback(int value) {
    return goDouble(value);
}
